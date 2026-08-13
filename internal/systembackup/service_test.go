package systembackup

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drakkar-media/drakkar/internal/config"
)

type recordedCommand struct {
	name string
	args []string
}

type fakeRunner struct {
	calls               []recordedCommand
	failDatabaseRestore int
}

func (r *fakeRunner) Run(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, recordedCommand{name: name, args: append([]string(nil), args...)})
	if name == "pg_restore" && containsArgumentPrefix(args, "--dbname=") && r.failDatabaseRestore > 0 {
		r.failDatabaseRestore--
		return nil, errors.New("injected database restore failure")
	}
	if name == "pg_dump" {
		for _, arg := range args {
			if strings.HasPrefix(arg, "--file=") {
				return nil, os.WriteFile(strings.TrimPrefix(arg, "--file="), []byte("PGDMP-test-data"), 0o600)
			}
		}
	}
	return nil, nil
}

func containsArgumentPrefix(args []string, prefix string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return true
		}
	}
	return false
}

func newTestService(t *testing.T) (*Service, config.Settings) {
	t.Helper()
	settingsPath := filepath.Join(t.TempDir(), settingsFile)
	cfg, err := config.LoadOrCreate(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(settingsPath, nil)
	service.runner = &fakeRunner{}
	return service, cfg
}

func TestServiceArchiveRoundTrip(t *testing.T) {
	service, _ := newTestService(t)
	created, err := service.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	items, err := service.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != created.Name || items[0].SizeBytes <= 0 {
		t.Fatalf("unexpected backup list: %+v", items)
	}

	var archive bytes.Buffer
	if err := service.WriteArchive(context.Background(), created.Name, &archive); err != nil {
		t.Fatal(err)
	}
	imported, _ := newTestService(t)
	info, err := imported.ImportArchive(context.Background(), bytes.NewReader(archive.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if info.SizeBytes != created.SizeBytes || info.DrakkarVersion != created.DrakkarVersion {
		t.Fatalf("import metadata mismatch: created=%+v imported=%+v", created, info)
	}
	if _, err := imported.StageRestore(context.Background(), info.Name); err != nil {
		t.Fatal(err)
	}
	status, err := imported.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "scheduled" || status.BackupName != info.Name {
		t.Fatalf("unexpected restore status: %+v", status)
	}
	if err := imported.Delete(context.Background(), info.Name); err == nil {
		t.Fatal("expected staged backup deletion to be rejected")
	}
}

func TestWriteArchiveValidatesBeforeStreaming(t *testing.T) {
	service, _ := newTestService(t)
	created, err := service.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(service.backupDir, created.Name, databaseFile), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	err = service.WriteArchive(context.Background(), created.Name, &archive)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum error, got %v", err)
	}
	if archive.Len() != 0 {
		t.Fatalf("invalid archive wrote %d bytes before validation", archive.Len())
	}
}

func TestImportArchiveRejectsUnsafeEntry(t *testing.T) {
	service, _ := newTestService(t)
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	body := []byte("unsafe")
	if err := tw.WriteHeader(&tar.Header{Name: "../settings.json", Mode: 0o600, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ImportArchive(context.Background(), bytes.NewReader(archive.Bytes())); err == nil {
		t.Fatal("expected unsafe archive entry to be rejected")
	}
}

func TestApplyPendingRestorePreservesInfrastructureAndStagesRebuild(t *testing.T) {
	service, backupCfg := newTestService(t)
	backupCfg.Seerr.URL = "http://backup-seerr"
	backupCfg.Seerr.APIKey = "backup-key"
	if err := config.Save(service.settingsPath, backupCfg); err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	current := backupCfg
	current.Seerr.URL = "http://current-seerr"
	current.Seerr.APIKey = "current-key"
	current.Database.Password = "current-database-secret"
	current.Valkey.Password = "current-valkey-secret"
	if err := config.Save(service.settingsPath, current); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StageRestore(context.Background(), created.Name); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	if err := applyPendingRestore(context.Background(), service.settingsPath, nil, runner); err != nil {
		t.Fatal(err)
	}

	restored, err := config.Load(service.settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Seerr.URL != backupCfg.Seerr.URL || restored.Seerr.APIKey != backupCfg.Seerr.APIKey {
		t.Fatalf("application settings were not restored: %+v", restored.Seerr)
	}
	if restored.Database != current.Database || restored.Valkey != current.Valkey {
		t.Fatal("deployment database/Valkey endpoints were not preserved")
	}
	if _, err := os.Stat(filepath.Join(service.restoreDir, "pending.json")); !os.IsNotExist(err) {
		t.Fatalf("pending marker still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(service.restoreDir, "rebuild-publications.json")); err != nil {
		t.Fatalf("publication rebuild marker missing: %v", err)
	}
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "rebuilding" {
		t.Fatalf("unexpected restore status: %+v", status)
	}
	if err := CompletePublicationRebuild(service.settingsPath); err != nil {
		t.Fatal(err)
	}
	status, err = service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "completed" || status.FinishedAt == nil {
		t.Fatalf("restore was not finalized: %+v", status)
	}

	commands := make([]string, 0, len(runner.calls))
	for _, call := range runner.calls {
		commands = append(commands, call.name)
	}
	joined := strings.Join(commands, ",")
	for _, expected := range []string{"pg_dump", "dropdb", "createdb", "pg_restore", "psql"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("restore did not run %s: %s", expected, joined)
		}
	}
}

func TestApplyPendingRestoreRollsBackDatabaseFailure(t *testing.T) {
	service, cfg := newTestService(t)
	created, err := service.Create(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg.Seerr.URL = "http://current-seerr"
	cfg.Seerr.APIKey = "current-key"
	if err := config.Save(service.settingsPath, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StageRestore(context.Background(), created.Name); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{failDatabaseRestore: 1}
	err = applyPendingRestore(context.Background(), service.settingsPath, nil, runner)
	if !errors.Is(err, ErrRestoreHandled) {
		t.Fatalf("expected safely handled restore error, got %v", err)
	}
	current, loadErr := config.Load(service.settingsPath)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if current.Seerr != cfg.Seerr {
		t.Fatalf("settings were not rolled back: %+v", current.Seerr)
	}
	status, statusErr := service.Status(context.Background())
	if statusErr != nil {
		t.Fatal(statusErr)
	}
	if status.State != "rolled_back" || status.Error == "" {
		t.Fatalf("unexpected rollback status: %+v", status)
	}
	if _, statErr := os.Stat(filepath.Join(service.restoreDir, "rebuild-publications.json")); statErr != nil {
		t.Fatalf("rollback publication marker missing: %v", statErr)
	}
}
