// Package systembackup creates portable Drakkar settings/database bundles and
// stages validated restores for the container's pre-start restore helper.
package systembackup

import (
	"archive/tar"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/drakkar-media/drakkar/internal/config"
	"github.com/drakkar-media/drakkar/internal/version"
)

const (
	manifestFile     = "manifest.json"
	settingsFile     = "settings.json"
	databaseFile     = "database.dump"
	formatVersion    = 1
	maxManifestBytes = int64(1 << 20)
	maxSettingsBytes = int64(16 << 20)
	// MaxArchiveBytes bounds uploaded backup data before tar extraction.
	MaxArchiveBytes = int64(128 << 30)
)

var backupNamePattern = regexp.MustCompile(`^drakkar-[A-Za-z0-9T_.-]{8,96}$`)

// BackupInfo is the API-safe metadata for one completed server-side bundle.
type BackupInfo struct {
	Name           string    `json:"name"`
	CreatedAt      time.Time `json:"createdAt"`
	SizeBytes      int64     `json:"sizeBytes"`
	DrakkarVersion string    `json:"drakkarVersion"`
}

// RestoreStatus records each staged restore through database replacement,
// publication rebuilding, completion, or rollback so the UI can report what
// happened across the container restart.
type RestoreStatus struct {
	State      string     `json:"state"`
	BackupName string     `json:"backupName,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Error      string     `json:"error,omitempty"`
}

// OperationStatus reports a long-running in-process backup operation. It is
// intentionally separate from RestoreStatus: restore status survives the
// restart, while create/import validation only needs process-local progress.
type OperationStatus struct {
	State      string      `json:"state"`
	Operation  string      `json:"operation,omitempty"`
	BackupName string      `json:"backupName,omitempty"`
	StartedAt  *time.Time  `json:"startedAt,omitempty"`
	FinishedAt *time.Time  `json:"finishedAt,omitempty"`
	Error      string      `json:"error,omitempty"`
	Backup     *BackupInfo `json:"backup,omitempty"`
}

type manifest struct {
	FormatVersion  int                `json:"formatVersion"`
	CreatedAt      time.Time          `json:"createdAt"`
	DrakkarVersion string             `json:"drakkarVersion"`
	DatabaseName   string             `json:"databaseName"`
	Files          map[string]fileSum `json:"files"`
}

type fileSum struct {
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}

type pendingRestore struct {
	BackupName  string    `json:"backupName"`
	RequestedAt time.Time `json:"requestedAt"`
}

type commandRunner interface {
	Run(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail != "" {
			return output, fmt.Errorf("%s: %w: %s", name, err, detail)
		}
		return output, fmt.Errorf("%s: %w", name, err)
	}
	return output, nil
}

// Service owns backup bundle creation/import and restore staging. Operations
// are serialized because pg_dump and full-bundle checksum validation can be
// disk intensive and must not overlap a restore request.
type Service struct {
	settingsPath string
	backupDir    string
	restoreDir   string
	runner       commandRunner
	restart      func()
	mu           sync.Mutex
	opMu         sync.Mutex
	operation    OperationStatus
}

// NewService creates a backup service rooted beside settingsPath. restart is
// called only after a restore marker and status have been durably staged.
func NewService(settingsPath string, restart func()) *Service {
	dataDir := filepath.Dir(settingsPath)
	return &Service{
		settingsPath: settingsPath,
		backupDir:    filepath.Join(dataDir, "backups"),
		restoreDir:   filepath.Join(dataDir, "restore"),
		runner:       execCommandRunner{},
		restart:      restart,
	}
}

// Create writes a settings snapshot and a PostgreSQL custom-format dump into
// an atomic server-side bundle directory.
func (s *Service) Create(ctx context.Context) (BackupInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.backupDir, 0o700); err != nil {
		return BackupInfo{}, err
	}
	tempDir, err := os.MkdirTemp(s.backupDir, ".creating-")
	if err != nil {
		return BackupInfo{}, err
	}
	defer os.RemoveAll(tempDir)

	settingsTarget := filepath.Join(tempDir, settingsFile)
	if err := copyRegularFile(s.settingsPath, settingsTarget, maxSettingsBytes); err != nil {
		return BackupInfo{}, fmt.Errorf("copy settings: %w", err)
	}
	cfg, err := config.LoadSnapshot(settingsTarget)
	if err != nil {
		return BackupInfo{}, fmt.Errorf("load settings snapshot: %w", err)
	}
	databaseTarget := filepath.Join(tempDir, databaseFile)
	if _, err := s.runner.Run(ctx, postgresEnv(cfg.Database), "pg_dump",
		"--format=custom", "--compress=6", "--no-owner", "--no-privileges",
		"--file="+databaseTarget,
	); err != nil {
		return BackupInfo{}, fmt.Errorf("database backup: %w", err)
	}
	if err := requireRegularNonempty(databaseTarget); err != nil {
		return BackupInfo{}, fmt.Errorf("database backup: %w", err)
	}
	if err := os.Chmod(databaseTarget, 0o600); err != nil {
		return BackupInfo{}, fmt.Errorf("secure database backup: %w", err)
	}

	createdAt := time.Now().UTC()
	m := manifest{
		FormatVersion:  formatVersion,
		CreatedAt:      createdAt,
		DrakkarVersion: version.Version,
		DatabaseName:   cfg.Database.Name,
		Files:          make(map[string]fileSum, 2),
	}
	for _, name := range []string{settingsFile, databaseFile} {
		sum, err := checksumFile(filepath.Join(tempDir, name))
		if err != nil {
			return BackupInfo{}, err
		}
		m.Files[name] = sum
	}
	if err := writeJSONAtomic(filepath.Join(tempDir, manifestFile), m); err != nil {
		return BackupInfo{}, err
	}

	name, err := newBackupName(createdAt)
	if err != nil {
		return BackupInfo{}, err
	}
	finalDir := filepath.Join(s.backupDir, name)
	if err := os.Rename(tempDir, finalDir); err != nil {
		return BackupInfo{}, err
	}
	return backupInfo(finalDir, name, m)
}

// StartCreate starts backup creation in a process-owned goroutine. The work
// uses context.Background so browser navigation or request timeout cannot
// cancel pg_dump after the operation has been accepted.
func (s *Service) StartCreate() (OperationStatus, error) {
	started := time.Now().UTC()
	status := OperationStatus{State: "creating", Operation: "create_backup", StartedAt: &started}
	if err := s.startOperation(status); err != nil {
		return OperationStatus{}, err
	}
	go func() {
		info, err := s.Create(context.Background())
		if err != nil {
			s.finishOperation(OperationStatus{State: "failed", Operation: "create_backup", Error: err.Error()})
			return
		}
		completed := OperationStatus{State: "completed", Operation: "create_backup", BackupName: info.Name, Backup: &info}
		s.finishOperation(completed)
	}()
	return status, nil
}

// List returns completed bundles newest first. Incomplete hidden directories
// are ignored and malformed visible bundles are omitted.
func (s *Service) List(_ context.Context) ([]BackupInfo, error) {
	entries, err := os.ReadDir(s.backupDir)
	if errors.Is(err, os.ErrNotExist) {
		return []BackupInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out []BackupInfo
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || !validBackupName(entry.Name()) {
			continue
		}
		dir := filepath.Join(s.backupDir, entry.Name())
		m, err := readManifest(dir)
		if err != nil {
			continue
		}
		info, err := backupInfo(dir, entry.Name(), m)
		if err == nil {
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// WriteArchive streams a validated bundle as an uncompressed tar archive.
// PostgreSQL's custom dump is already compressed, avoiding a second full-size
// temporary archive on disk.
func (s *Service) WriteArchive(ctx context.Context, name string, dst io.Writer) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir, err := s.bundlePath(name)
	if err != nil {
		return err
	}
	if _, err := s.validateBundle(ctx, dir); err != nil {
		return err
	}
	tw := tar.NewWriter(dst)
	for _, fileName := range []string{manifestFile, settingsFile, databaseFile} {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(dir, fileName)
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		header := &tar.Header{Name: fileName, Mode: 0o600, Size: info.Size(), ModTime: info.ModTime()}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return tw.Close()
}

// ImportArchive validates and atomically installs an uploaded tar bundle.
func (s *Service) ImportArchive(ctx context.Context, src io.Reader) (BackupInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.backupDir, 0o700); err != nil {
		return BackupInfo{}, err
	}
	tempDir, err := os.MkdirTemp(s.backupDir, ".uploading-")
	if err != nil {
		return BackupInfo{}, err
	}
	defer os.RemoveAll(tempDir)

	tr := tar.NewReader(io.LimitReader(src, MaxArchiveBytes+1))
	seen := make(map[string]bool, 3)
	var total int64
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return BackupInfo{}, fmt.Errorf("read backup archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg || !allowedArchiveFile(header.Name) || seen[header.Name] {
			return BackupInfo{}, fmt.Errorf("invalid backup archive entry %q", header.Name)
		}
		limit := maxArchiveFileSize(header.Name)
		if header.Size < 0 || header.Size > limit || total > MaxArchiveBytes-header.Size {
			return BackupInfo{}, fmt.Errorf("backup archive entry %q is too large", header.Name)
		}
		total += header.Size
		target := filepath.Join(tempDir, header.Name)
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return BackupInfo{}, err
		}
		_, copyErr := io.CopyN(file, tr, header.Size)
		closeErr := file.Close()
		if copyErr != nil {
			return BackupInfo{}, copyErr
		}
		if closeErr != nil {
			return BackupInfo{}, closeErr
		}
		seen[header.Name] = true
	}
	for _, required := range []string{manifestFile, settingsFile, databaseFile} {
		if !seen[required] {
			return BackupInfo{}, fmt.Errorf("backup archive is missing %s", required)
		}
	}
	m, err := s.validateBundle(ctx, tempDir)
	if err != nil {
		return BackupInfo{}, err
	}
	name, err := newBackupName(time.Now().UTC())
	if err != nil {
		return BackupInfo{}, err
	}
	finalDir := filepath.Join(s.backupDir, name)
	if err := os.Rename(tempDir, finalDir); err != nil {
		return BackupInfo{}, err
	}
	return backupInfo(finalDir, name, m)
}

// Delete removes a completed bundle unless it is currently staged for
// restore.
func (s *Service) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.bundlePath(name)
	if err != nil {
		return err
	}
	if pending, err := readPending(s.restoreDir); err == nil && pending.BackupName == name {
		return errors.New("backup is staged for restore")
	}
	return os.RemoveAll(dir)
}

// StageRestore fully validates name, writes the persistent pending marker, and
// requests a graceful process restart. The pre-start helper performs restore
// while no Drakkar database connections or workers are active.
func (s *Service) StageRestore(ctx context.Context, name string) (RestoreStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.bundlePath(name)
	if err != nil {
		return RestoreStatus{}, err
	}
	if _, err := s.validateBundle(ctx, dir); err != nil {
		return RestoreStatus{}, err
	}
	if _, err := readPending(s.restoreDir); err == nil {
		return RestoreStatus{}, errors.New("a restore is already pending")
	} else if !errors.Is(err, os.ErrNotExist) {
		return RestoreStatus{}, err
	}
	if err := os.MkdirAll(s.restoreDir, 0o700); err != nil {
		return RestoreStatus{}, err
	}
	now := time.Now().UTC()
	if err := writeJSONAtomic(filepath.Join(s.restoreDir, "pending.json"), pendingRestore{BackupName: name, RequestedAt: now}); err != nil {
		return RestoreStatus{}, err
	}
	status := RestoreStatus{State: "scheduled", BackupName: name, StartedAt: &now}
	if err := writeJSONAtomic(filepath.Join(s.restoreDir, "status.json"), status); err != nil {
		_ = os.Remove(filepath.Join(s.restoreDir, "pending.json"))
		return RestoreStatus{}, err
	}
	if s.restart != nil {
		go func() {
			time.Sleep(time.Second)
			s.restart()
		}()
	}
	return status, nil
}

// StartRestore validates and stages a restore in the background, then lets the
// existing staged-restore restart path take over.
func (s *Service) StartRestore(name string) (OperationStatus, error) {
	started := time.Now().UTC()
	status := OperationStatus{State: "validating_restore", Operation: "restore_backup", BackupName: name, StartedAt: &started}
	if err := s.startOperation(status); err != nil {
		return OperationStatus{}, err
	}
	go func() {
		restoreStatus, err := s.StageRestore(context.Background(), name)
		if err != nil {
			s.finishOperation(OperationStatus{State: "failed", Operation: "restore_backup", BackupName: name, Error: err.Error()})
			return
		}
		s.finishOperation(OperationStatus{State: restoreStatus.State, Operation: "restore_backup", BackupName: name})
	}()
	return status, nil
}

// Status returns the latest persisted restore status, or idle before the first
// restore attempt.
func (s *Service) Status(_ context.Context) (RestoreStatus, error) {
	var status RestoreStatus
	err := readJSON(filepath.Join(s.restoreDir, "status.json"), maxManifestBytes, &status)
	if errors.Is(err, os.ErrNotExist) {
		return RestoreStatus{State: "idle"}, nil
	}
	return status, err
}

// OperationStatus returns the current or most recent in-process backup
// operation. It is deliberately memory-only; completed backup bundles and
// staged restore status remain the durable records.
func (s *Service) OperationStatus(_ context.Context) OperationStatus {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.operation.State == "" {
		return OperationStatus{State: "idle"}
	}
	return s.operation
}

func (s *Service) startOperation(status OperationStatus) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if backupOperationActive(s.operation.State) {
		return errors.New("a backup or restore operation is already running")
	}
	s.operation = status
	return nil
}

func (s *Service) finishOperation(status OperationStatus) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	finished := time.Now().UTC()
	status.StartedAt = s.operation.StartedAt
	status.FinishedAt = &finished
	if status.BackupName == "" {
		status.BackupName = s.operation.BackupName
	}
	s.operation = status
}

func backupOperationActive(state string) bool {
	switch state {
	case "creating", "validating_restore", "scheduled", "restoring", "rebuilding":
		return true
	default:
		return false
	}
}

func (s *Service) validateBundle(ctx context.Context, dir string) (manifest, error) {
	m, err := readManifest(dir)
	if err != nil {
		return manifest{}, err
	}
	if m.FormatVersion != formatVersion {
		return manifest{}, fmt.Errorf("unsupported backup format version %d", m.FormatVersion)
	}
	for _, name := range []string{settingsFile, databaseFile} {
		expected, ok := m.Files[name]
		if !ok {
			return manifest{}, fmt.Errorf("backup manifest is missing %s", name)
		}
		actual, err := checksumFile(filepath.Join(dir, name))
		if err != nil {
			return manifest{}, err
		}
		if actual != expected {
			return manifest{}, fmt.Errorf("backup checksum mismatch for %s", name)
		}
	}
	cfg, err := config.LoadSnapshot(filepath.Join(dir, settingsFile))
	if err != nil {
		return manifest{}, fmt.Errorf("backup settings: %w", err)
	}
	if strings.TrimSpace(m.DatabaseName) == "" || m.DatabaseName != cfg.Database.Name {
		return manifest{}, errors.New("backup database name does not match settings")
	}
	if _, err := s.runner.Run(ctx, nil, "pg_restore", "--list", filepath.Join(dir, databaseFile)); err != nil {
		return manifest{}, fmt.Errorf("backup database dump: %w", err)
	}
	return m, nil
}

func (s *Service) bundlePath(name string) (string, error) {
	if !validBackupName(name) {
		return "", errors.New("invalid backup name")
	}
	dir := filepath.Join(s.backupDir, name)
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("backup is not a directory")
	}
	return dir, nil
}

func postgresEnv(cfg config.DatabaseConfig) []string {
	return []string{
		"PGHOST=" + cfg.Host,
		fmt.Sprintf("PGPORT=%d", cfg.Port),
		"PGDATABASE=" + cfg.Name,
		"PGUSER=" + cfg.Username,
		"PGPASSWORD=" + cfg.Password,
		"PGSSLMODE=disable",
	}
}

func readManifest(dir string) (manifest, error) {
	var m manifest
	err := readJSON(filepath.Join(dir, manifestFile), maxManifestBytes, &m)
	return m, err
}

func backupInfo(dir, name string, m manifest) (BackupInfo, error) {
	var size int64
	for _, fileName := range []string{manifestFile, settingsFile, databaseFile} {
		info, err := os.Stat(filepath.Join(dir, fileName))
		if err != nil {
			return BackupInfo{}, err
		}
		size += info.Size()
	}
	return BackupInfo{Name: name, CreatedAt: m.CreatedAt, SizeBytes: size, DrakkarVersion: m.DrakkarVersion}, nil
}

func checksumFile(path string) (fileSum, error) {
	file, err := os.Open(path)
	if err != nil {
		return fileSum{}, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return fileSum{}, err
	}
	return fileSum{SizeBytes: size, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func copyRegularFile(source, target string, maxBytes int64) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBytes {
		return errors.New("source is not a bounded regular file")
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	return errors.Join(copyErr, closeErr)
}

func requireRegularNonempty(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return errors.New("output is empty or not a regular file")
	}
	return nil
}

func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".json-")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(body); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}

func readJSON(path string, maxBytes int64, dst any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBytes {
		return errors.New("JSON source is not a bounded regular file")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxBytes+1))
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("JSON contains trailing data")
		}
		return err
	}
	return nil
}

func readPending(restoreDir string) (pendingRestore, error) {
	var pending pendingRestore
	err := readJSON(filepath.Join(restoreDir, "pending.json"), maxManifestBytes, &pending)
	return pending, err
}

func newBackupName(at time.Time) (string, error) {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("drakkar-%s-%s", at.UTC().Format("20060102T150405Z"), hex.EncodeToString(suffix[:])), nil
}

func validBackupName(name string) bool {
	return backupNamePattern.MatchString(name) && filepath.Base(name) == name
}

func allowedArchiveFile(name string) bool {
	return name == manifestFile || name == settingsFile || name == databaseFile
}

func maxArchiveFileSize(name string) int64 {
	switch name {
	case manifestFile:
		return maxManifestBytes
	case settingsFile:
		return maxSettingsBytes
	default:
		return MaxArchiveBytes
	}
}
