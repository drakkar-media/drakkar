package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"github.com/drakkar-media/drakkar/internal/systembackup"
	"github.com/go-chi/chi/v5"
)

// SystemBackupService defines the administrative backup and staged-restore
// operations optionally exposed by a SettingsService implementation.
type SystemBackupService interface {
	ListBackups(ctx context.Context) ([]systembackup.BackupInfo, error)
	CreateBackup(ctx context.Context) (systembackup.BackupInfo, error)
	WriteBackupArchive(ctx context.Context, name string, dst io.Writer) error
	ImportBackupArchive(ctx context.Context, src io.Reader) (systembackup.BackupInfo, error)
	DeleteBackup(ctx context.Context, name string) error
	StageBackupRestore(ctx context.Context, name string) (systembackup.RestoreStatus, error)
	BackupRestoreStatus(ctx context.Context) (systembackup.RestoreStatus, error)
}

type backupDownloadWriter struct {
	http.ResponseWriter
	written int64
}

func (w *backupDownloadWriter) Write(body []byte) (int, error) {
	n, err := w.ResponseWriter.Write(body)
	w.written += int64(n)
	return n, err
}

func registerSystemBackupRoutes(r chi.Router, service SystemBackupService) {
	r.Get("/api/system/backups", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		items, err := service.ListBackups(r.Context())
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{"items": items})
	}))
	r.Post("/api/system/backups", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		item, err := service.CreateBackup(r.Context())
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		respondJSON(w, http.StatusCreated, item)
	}))
	r.Get("/api/system/backups/{name}/download", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/x-tar")
		disposition := mime.FormatMediaType("attachment", map[string]string{"filename": name + ".drakkar-backup"})
		w.Header().Set("Content-Disposition", disposition)
		stream := &backupDownloadWriter{ResponseWriter: w}
		if err := service.WriteBackupArchive(r.Context(), name, stream); err != nil {
			if stream.written == 0 {
				w.Header().Del("Content-Disposition")
				respondError(w, http.StatusInternalServerError, err)
				return
			}
			// Headers may already be committed if a disk/read error occurs during
			// streaming; logging is safer than appending JSON into a corrupt tar.
			slog.Error("backup download failed", "err", err, "backup", name)
		}
	}))
	r.Post("/api/system/backups/upload", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		reader, err := r.MultipartReader()
		if err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		for {
			part, nextErr := reader.NextPart()
			if errors.Is(nextErr, io.EOF) {
				respondError(w, http.StatusBadRequest, errors.New("backup file is required"))
				return
			}
			if nextErr != nil {
				respondError(w, http.StatusBadRequest, nextErr)
				return
			}
			if part.FormName() != "backup" {
				part.Close()
				continue
			}
			item, importErr := service.ImportBackupArchive(r.Context(), part)
			part.Close()
			if importErr != nil {
				respondError(w, http.StatusBadRequest, importErr)
				return
			}
			respondJSON(w, http.StatusCreated, item)
			return
		}
	}))
	r.Delete("/api/system/backups/{name}", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		if err := service.DeleteBackup(r.Context(), chi.URLParam(r, "name")); err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	r.Post("/api/system/backups/{name}/restore", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		var body struct {
			Confirmation string `json:"confirmation"`
		}
		if err := decodeJSONBody(r, &body); err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(body.Confirmation) != name {
			respondError(w, http.StatusBadRequest, fmt.Errorf("confirmation must exactly match backup name %q", name))
			return
		}
		status, err := service.StageBackupRestore(r.Context(), name)
		if err != nil {
			respondError(w, http.StatusBadRequest, err)
			return
		}
		respondJSON(w, http.StatusAccepted, status)
	}))
	r.Get("/api/system/restore-status", requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		status, err := service.BackupRestoreStatus(r.Context())
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		respondJSON(w, http.StatusOK, status)
	}))
}
