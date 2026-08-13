package systembackup

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/drakkar-media/drakkar/internal/config"
)

// ErrRestoreHandled means a staged restore failed before completion but the
// previous database/settings were left intact or successfully rolled back, so
// the normal Drakkar process may still start.
var ErrRestoreHandled = errors.New("restore failed and was safely handled")

// ApplyPendingRestore applies a staged bundle before the main Drakkar process
// starts. It creates a rollback dump first, restores into a freshly-created
// database, preserves current infrastructure credentials, and marks all media
// publications for startup symlink reconstruction.
func ApplyPendingRestore(ctx context.Context, settingsPath string, logOutput io.Writer) error {
	return applyPendingRestore(ctx, settingsPath, logOutput, execCommandRunner{})
}

func applyPendingRestore(ctx context.Context, settingsPath string, logOutput io.Writer, runner commandRunner) error {
	dataDir := filepath.Dir(settingsPath)
	restoreDir := filepath.Join(dataDir, "restore")
	if _, err := os.Stat(filepath.Join(restoreDir, "fatal")); err == nil {
		return errors.New("restore is in a fatal state; inspect restore/status.json and remove restore/fatal after repair")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	pending, err := readPending(restoreDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !validBackupName(pending.BackupName) {
		return handlePreRestoreFailure(restoreDir, pending.BackupName, errors.New("pending restore has invalid backup name"))
	}

	now := time.Now().UTC()
	status := RestoreStatus{State: "restoring", BackupName: pending.BackupName, StartedAt: &now}
	if err := writeJSONAtomic(filepath.Join(restoreDir, "status.json"), status); err != nil {
		return err
	}
	logRestore(logOutput, "validating backup %s", pending.BackupName)
	service := NewService(settingsPath, nil)
	service.runner = runner
	bundleDir := filepath.Join(service.backupDir, pending.BackupName)
	if _, err := service.validateBundle(ctx, bundleDir); err != nil {
		return handlePreRestoreFailure(restoreDir, pending.BackupName, err)
	}
	current, err := config.Load(settingsPath)
	if err != nil {
		return handlePreRestoreFailure(restoreDir, pending.BackupName, fmt.Errorf("load current settings: %w", err))
	}
	restored, err := config.LoadSnapshot(filepath.Join(bundleDir, settingsFile))
	if err != nil {
		return handlePreRestoreFailure(restoreDir, pending.BackupName, fmt.Errorf("load backup settings: %w", err))
	}

	rollbackDir, err := os.MkdirTemp(restoreDir, ".rollback-")
	if err != nil {
		return handlePreRestoreFailure(restoreDir, pending.BackupName, err)
	}
	defer os.RemoveAll(rollbackDir)
	rollbackSettings := filepath.Join(rollbackDir, settingsFile)
	rollbackDatabase := filepath.Join(rollbackDir, databaseFile)
	if err := copyRegularFile(settingsPath, rollbackSettings, maxSettingsBytes); err != nil {
		return handlePreRestoreFailure(restoreDir, pending.BackupName, fmt.Errorf("stage rollback settings: %w", err))
	}
	logRestore(logOutput, "creating rollback database dump")
	if _, err := service.runner.Run(ctx, postgresEnv(current.Database), "pg_dump",
		"--format=custom", "--compress=6", "--no-owner", "--no-privileges",
		"--file="+rollbackDatabase,
	); err != nil {
		return handlePreRestoreFailure(restoreDir, pending.BackupName, fmt.Errorf("stage rollback database: %w", err))
	}
	if err := requireRegularNonempty(rollbackDatabase); err != nil {
		return handlePreRestoreFailure(restoreDir, pending.BackupName, fmt.Errorf("stage rollback database: %w", err))
	}
	if err := os.Chmod(rollbackDatabase, 0o600); err != nil {
		return handlePreRestoreFailure(restoreDir, pending.BackupName, fmt.Errorf("secure rollback database: %w", err))
	}

	paths, err := currentPublicationPaths(ctx, service.runner, current.Database)
	if err != nil {
		return handlePreRestoreFailure(restoreDir, pending.BackupName, fmt.Errorf("list current publications: %w", err))
	}
	if err := validateTrackedSymlinks(paths); err != nil {
		return handlePreRestoreFailure(restoreDir, pending.BackupName, fmt.Errorf("validate current publications: %w", err))
	}
	if err := removeTrackedSymlinks(paths); err != nil {
		return rollbackRestore(ctx, service, current, rollbackSettings, rollbackDatabase, pending.BackupName, fmt.Errorf("remove current publications: %w", err), logOutput)
	}

	logRestore(logOutput, "restoring database")
	if err := replaceDatabase(ctx, service.runner, current.Database, filepath.Join(bundleDir, databaseFile)); err != nil {
		return rollbackRestore(ctx, service, current, rollbackSettings, rollbackDatabase, pending.BackupName, err, logOutput)
	}
	if _, err := service.runner.Run(ctx, postgresEnv(current.Database), "psql",
		"--set=ON_ERROR_STOP=1", "--command=delete from symlink_publications",
	); err != nil {
		return rollbackRestore(ctx, service, current, rollbackSettings, rollbackDatabase, pending.BackupName, fmt.Errorf("invalidate restored publications: %w", err), logOutput)
	}

	// Database and Valkey endpoints belong to the deployment being restored
	// into. Reusing credentials from another/older deployment could make the
	// restarted application abandon the database that was just restored.
	restored.Database = current.Database
	restored.Valkey = current.Valkey
	if err := config.Save(settingsPath, restored); err != nil {
		return rollbackRestore(ctx, service, current, rollbackSettings, rollbackDatabase, pending.BackupName, fmt.Errorf("restore settings: %w", err), logOutput)
	}
	if err := writeJSONAtomic(filepath.Join(restoreDir, "rebuild-publications.json"), map[string]any{
		"backupName": pending.BackupName,
		"createdAt":  time.Now().UTC(),
	}); err != nil {
		return rollbackRestore(ctx, service, current, rollbackSettings, rollbackDatabase, pending.BackupName, fmt.Errorf("stage publication rebuild: %w", err), logOutput)
	}

	status = RestoreStatus{State: "rebuilding", BackupName: pending.BackupName, StartedAt: &now}
	if err := writeJSONAtomic(filepath.Join(restoreDir, "status.json"), status); err != nil {
		return rollbackRestore(ctx, service, current, rollbackSettings, rollbackDatabase, pending.BackupName, fmt.Errorf("persist publication rebuild status: %w", err), logOutput)
	}
	if err := os.Remove(filepath.Join(restoreDir, "pending.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return rollbackRestore(ctx, service, current, rollbackSettings, rollbackDatabase, pending.BackupName, fmt.Errorf("clear pending restore: %w", err), logOutput)
	}
	logRestore(logOutput, "database and settings restored; Drakkar will rebuild symlinks on startup")
	return nil
}

// CompletePublicationRebuild marks a restored bundle complete and clears its
// rebuild marker. Callers must only invoke it after symlink reconstruction and
// configured media-server refreshes have all succeeded.
func CompletePublicationRebuild(settingsPath string) error {
	restoreDir := filepath.Join(filepath.Dir(settingsPath), "restore")
	var status RestoreStatus
	if err := readJSON(filepath.Join(restoreDir, "status.json"), maxManifestBytes, &status); err != nil {
		return err
	}
	if status.State == "rebuilding" {
		finished := time.Now().UTC()
		status.State = "completed"
		status.FinishedAt = &finished
		status.Error = ""
		if err := writeJSONAtomic(filepath.Join(restoreDir, "status.json"), status); err != nil {
			return err
		}
	}
	marker := filepath.Join(restoreDir, "rebuild-publications.json")
	if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func replaceDatabase(ctx context.Context, runner commandRunner, cfg config.DatabaseConfig, dumpPath string) error {
	env := postgresEnv(cfg)
	if _, err := runner.Run(ctx, env, "dropdb", "--if-exists", "--force", "--maintenance-db=postgres", "--", cfg.Name); err != nil {
		return fmt.Errorf("drop target database: %w", err)
	}
	if _, err := runner.Run(ctx, env, "createdb", "--owner="+cfg.Username, "--maintenance-db=postgres", "--", cfg.Name); err != nil {
		return fmt.Errorf("create target database: %w", err)
	}
	if _, err := runner.Run(ctx, env, "pg_restore",
		"--exit-on-error", "--no-owner", "--no-privileges", "--dbname="+cfg.Name, dumpPath,
	); err != nil {
		return fmt.Errorf("restore database dump: %w", err)
	}
	return nil
}

func rollbackRestore(ctx context.Context, service *Service, current config.Settings, settingsPath, databasePath, backupName string, restoreErr error, logOutput io.Writer) error {
	logRestore(logOutput, "restore failed; rolling back: %v", restoreErr)
	rollbackErr := replaceDatabase(ctx, service.runner, current.Database, databasePath)
	if rollbackErr == nil {
		rollbackErr = copyRegularFileReplacing(settingsPath, service.settingsPath, maxSettingsBytes)
	}
	if rollbackErr == nil {
		_, rollbackErr = service.runner.Run(ctx, postgresEnv(current.Database), "psql",
			"--set=ON_ERROR_STOP=1", "--command=delete from symlink_publications",
		)
	}
	if rollbackErr == nil {
		rollbackErr = writeJSONAtomic(filepath.Join(service.restoreDir, "rebuild-publications.json"), map[string]any{
			"reason":    "restore rollback",
			"createdAt": time.Now().UTC(),
		})
	}
	if rollbackErr == nil {
		if err := os.Remove(filepath.Join(service.restoreDir, "pending.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErr = fmt.Errorf("clear pending restore after rollback: %w", err)
		}
	}
	finished := time.Now().UTC()
	state := "rolled_back"
	statusErr := restoreErr.Error()
	if rollbackErr != nil {
		state = "fatal"
		statusErr = errors.Join(restoreErr, fmt.Errorf("rollback failed: %w", rollbackErr)).Error()
	}
	status := RestoreStatus{State: state, BackupName: backupName, FinishedAt: &finished, Error: statusErr}
	_ = writeJSONAtomic(filepath.Join(service.restoreDir, "status.json"), status)
	if rollbackErr != nil {
		_ = os.WriteFile(filepath.Join(service.restoreDir, "fatal"), []byte(statusErr+"\n"), 0o600)
		return errors.New(statusErr)
	}
	return fmt.Errorf("%w: %v", ErrRestoreHandled, restoreErr)
}

func handlePreRestoreFailure(restoreDir, backupName string, restoreErr error) error {
	finished := time.Now().UTC()
	state := "failed"
	if err := os.Remove(filepath.Join(restoreDir, "pending.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		restoreErr = errors.Join(restoreErr, fmt.Errorf("clear failed pending restore: %w", err))
		state = "fatal"
		_ = os.WriteFile(filepath.Join(restoreDir, "fatal"), []byte(restoreErr.Error()+"\n"), 0o600)
	}
	_ = writeJSONAtomic(filepath.Join(restoreDir, "status.json"), RestoreStatus{
		State: state, BackupName: backupName, FinishedAt: &finished, Error: restoreErr.Error(),
	})
	if state == "fatal" {
		return restoreErr
	}
	return fmt.Errorf("%w: %v", ErrRestoreHandled, restoreErr)
}

func currentPublicationPaths(ctx context.Context, runner commandRunner, cfg config.DatabaseConfig) ([]string, error) {
	output, err := runner.Run(ctx, postgresEnv(cfg), "psql", "--set=ON_ERROR_STOP=1", "--quiet", "--command=copy (select library_path from symlink_publications order by library_path) to stdout with (format csv)")
	if err != nil {
		return nil, err
	}
	reader := csv.NewReader(strings.NewReader(string(output)))
	var paths []string
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(record) == 1 && strings.TrimSpace(record[0]) != "" {
			paths = append(paths, record[0])
		}
	}
	return paths, nil
}

func validateTrackedSymlinks(paths []string) error {
	runtime := config.DefaultRuntime()
	roots := []string{runtime.MovieLibraryPath, runtime.TVLibraryPath}
	var validationErrors []error
	for _, path := range paths {
		if !insideAnyRoot(path, roots) {
			validationErrors = append(validationErrors, fmt.Errorf("publication is outside managed roots: %s", path))
			continue
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			validationErrors = append(validationErrors, fmt.Errorf("inspect publication %s: %w", path, err))
			continue
		}
		if info.Mode()&os.ModeSymlink == 0 {
			validationErrors = append(validationErrors, fmt.Errorf("publication is not a symlink: %s", path))
			continue
		}
	}
	if err := errors.Join(validationErrors...); err != nil {
		return err
	}
	return nil
}

func removeTrackedSymlinks(paths []string) error {
	for _, path := range paths {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect publication %s before removal: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("publication changed to a non-symlink before removal: %s", path)
		}
		if err := os.Remove(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("remove publication %s: %w", path, err)
		}
	}
	return nil
}

func insideAnyRoot(path string, roots []string) bool {
	path = filepath.Clean(path)
	for _, root := range roots {
		root = filepath.Clean(root)
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func copyRegularFileReplacing(source, target string, maxBytes int64) error {
	temp, err := os.CreateTemp(filepath.Dir(target), ".restore-settings-")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	if err := temp.Close(); err != nil {
		return err
	}
	_ = os.Remove(tempPath)
	defer os.Remove(tempPath)
	if err := copyRegularFile(source, tempPath, maxBytes); err != nil {
		return err
	}
	return os.Rename(tempPath, target)
}

func logRestore(output io.Writer, format string, args ...any) {
	if output != nil {
		_, _ = fmt.Fprintf(output, "drakkar restore: "+format+"\n", args...)
	}
}
