package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drakkar-media/drakkar/internal/auth"
	"github.com/drakkar-media/drakkar/internal/systembackup"
	"github.com/go-chi/chi/v5"
)

type systemBackupStub struct {
	items          []systembackup.BackupInfo
	restoreStatus  systembackup.RestoreStatus
	downloadBody   string
	downloadErr    error
	stagedName     string
	importedBackup systembackup.BackupInfo
}

func (s *systemBackupStub) ListBackups(context.Context) ([]systembackup.BackupInfo, error) {
	return s.items, nil
}
func (s *systemBackupStub) CreateBackup(context.Context) (systembackup.BackupInfo, error) {
	return s.items[0], nil
}
func (s *systemBackupStub) WriteBackupArchive(_ context.Context, _ string, dst io.Writer) error {
	if s.downloadBody != "" {
		_, _ = io.WriteString(dst, s.downloadBody)
	}
	return s.downloadErr
}
func (s *systemBackupStub) ImportBackupArchive(context.Context, io.Reader) (systembackup.BackupInfo, error) {
	return s.importedBackup, nil
}
func (s *systemBackupStub) DeleteBackup(context.Context, string) error { return nil }
func (s *systemBackupStub) StageBackupRestore(_ context.Context, name string) (systembackup.RestoreStatus, error) {
	s.stagedName = name
	return s.restoreStatus, nil
}
func (s *systemBackupStub) BackupRestoreStatus(context.Context) (systembackup.RestoreStatus, error) {
	return s.restoreStatus, nil
}

func backupTestRequest(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	return req.WithContext(auth.NewContext(req.Context(), auth.Claims{Role: "admin"}))
}

func TestSystemBackupRoutesRequireAdminAndConfirmRestore(t *testing.T) {
	name := "drakkar-20260813T120000Z-12345678"
	service := &systemBackupStub{
		items:         []systembackup.BackupInfo{{Name: name, CreatedAt: time.Now().UTC()}},
		restoreStatus: systembackup.RestoreStatus{State: "scheduled", BackupName: name},
	}
	router := chi.NewRouter()
	registerSystemBackupRoutes(router, service)

	forbidden := httptest.NewRecorder()
	router.ServeHTTP(forbidden, httptest.NewRequest(http.MethodGet, "/api/system/backups", nil))
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("non-admin status = %d, want %d", forbidden.Code, http.StatusForbidden)
	}

	mismatch := httptest.NewRecorder()
	mismatchRequest := backupTestRequest(http.MethodPost, "/api/system/backups/"+name+"/restore", `{"confirmation":"wrong"}`)
	mismatchRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(mismatch, mismatchRequest)
	if mismatch.Code != http.StatusBadRequest || service.stagedName != "" {
		t.Fatalf("mismatched restore was accepted: status=%d staged=%q", mismatch.Code, service.stagedName)
	}

	accepted := httptest.NewRecorder()
	acceptedRequest := backupTestRequest(http.MethodPost, "/api/system/backups/"+name+"/restore", `{"confirmation":"`+name+`"}`)
	acceptedRequest.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(accepted, acceptedRequest)
	if accepted.Code != http.StatusAccepted || service.stagedName != name {
		t.Fatalf("confirmed restore was not staged: status=%d staged=%q body=%s", accepted.Code, service.stagedName, accepted.Body.String())
	}
}

func TestBackupDownloadReportsPreStreamValidationError(t *testing.T) {
	service := &systemBackupStub{downloadErr: errors.New("checksum mismatch")}
	router := chi.NewRouter()
	registerSystemBackupRoutes(router, service)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, backupTestRequest(http.MethodGet, "/api/system/backups/drakkar-20260813T120000Z-12345678/download", ""))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("download status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if response.Header().Get("Content-Disposition") != "" || !strings.Contains(response.Body.String(), "checksum mismatch") {
		t.Fatalf("unexpected error response: headers=%v body=%s", response.Header(), response.Body.String())
	}
}
