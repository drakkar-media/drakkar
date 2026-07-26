// Package nzb parses NZB documents and imports them into the queue, whether
// uploaded manually, supplied as a local file path, or fetched automatically
// by an indexer.
package nzb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/drakkar-media/drakkar/internal/archive"
	"github.com/drakkar-media/drakkar/internal/database"
)

var (
	// ErrUploadTooLarge indicates an NZB upload or file exceeded the
	// configured maximum size before it could be fully read.
	ErrUploadTooLarge = errors.New("nzb upload too large")
	// ErrEmptyDocument indicates an NZB upload or file contained zero bytes.
	ErrEmptyDocument = errors.New("nzb document empty")
)

// ImportSink is the persistence dependency required to import an NZB: it
// stores the parsed document and marks the resulting queue item as indexed.
type ImportSink interface {
	CreateImportedNZB(ctx context.Context, imported database.ImportedNZB) (database.QueueSnapshot, error)
	SetImportedNZBIndexed(ctx context.Context, queueItemID int64) error
}

// Importer stages and parses manually uploaded or locally-provided NZB
// files before handing them to an ImportSink for persistence.
type Importer struct {
	stagingDir     string
	maxUploadBytes int64
}

// NewImporter creates an Importer that stages uploads under stagingDir and
// rejects any input exceeding maxUploadBytes.
func NewImporter(stagingDir string, maxUploadBytes int64) *Importer {
	return &Importer{
		stagingDir:     stagingDir,
		maxUploadBytes: maxUploadBytes,
	}
}

// Import stages src (e.g. an HTTP upload) to a temp file under the staging
// directory while hashing and size-limiting it, then parses and persists the
// result via sink. Returns ErrEmptyDocument or ErrUploadTooLarge if the
// size constraints are violated.
func (i *Importer) Import(ctx context.Context, sink ImportSink, fileName string, src io.Reader) (database.QueueSnapshot, error) {
	if strings.TrimSpace(fileName) == "" {
		fileName = "imported.nzb"
	}
	if err := os.MkdirAll(i.stagingDir, 0o755); err != nil {
		return database.QueueSnapshot{}, err
	}

	stageFile, err := os.CreateTemp(i.stagingDir, "*.nzb")
	if err != nil {
		return database.QueueSnapshot{}, err
	}
	stagePath := stageFile.Name()
	defer func() {
		stageFile.Close()
		_ = os.Remove(stagePath)
	}()

	hasher := sha256.New()
	limited := io.LimitReader(src, i.maxUploadBytes+1)
	written, err := io.Copy(io.MultiWriter(stageFile, hasher), limited)
	if err != nil {
		return database.QueueSnapshot{}, err
	}
	if written == 0 {
		return database.QueueSnapshot{}, ErrEmptyDocument
	}
	if written > i.maxUploadBytes {
		return database.QueueSnapshot{}, ErrUploadTooLarge
	}
	return i.importFromOpenFile(ctx, sink, sanitizeFileName(fileName), stageFile, hasher)
}

// MaxUploadBytes returns the configured maximum accepted upload size, in
// bytes.
func (i *Importer) MaxUploadBytes() int64 {
	return i.maxUploadBytes
}

// ImportPath imports an NZB that already exists on disk at path, applying
// the same size checks as Import but reading directly from that file
// instead of staging a copy first.
func (i *Importer) ImportPath(ctx context.Context, sink ImportSink, fileName, path string) (database.QueueSnapshot, error) {
	file, err := os.Open(path)
	if err != nil {
		return database.QueueSnapshot{}, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return database.QueueSnapshot{}, err
	}
	if info.Size() == 0 {
		return database.QueueSnapshot{}, ErrEmptyDocument
	}
	if info.Size() > i.maxUploadBytes {
		return database.QueueSnapshot{}, ErrUploadTooLarge
	}

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return database.QueueSnapshot{}, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return database.QueueSnapshot{}, err
	}
	return i.importFromOpenFile(ctx, sink, sanitizeFileName(fileName), file, hasher)
}

// importFromOpenFile is the shared tail end of Import and ImportPath: it
// parses an already-open, already-hashed NZB file and persists it via sink.
func (i *Importer) importFromOpenFile(ctx context.Context, sink ImportSink, fileName string, file *os.File, hasher hash.Hash) (database.QueueSnapshot, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return database.QueueSnapshot{}, err
	}

	// Read the file once and parse from the in-memory bytes — Parse used to
	// be given the open file handle directly, and a full second os.ReadFile
	// of the same path ran afterward just to get the raw XML for storage. For
	// a large season-pack NZB (can be several MB) that's a redundant full
	// disk read + allocation on every import.
	raw, err := io.ReadAll(file)
	if err != nil {
		return database.QueueSnapshot{}, err
	}
	document, err := Parse(bytes.NewReader(raw))
	if err != nil {
		return database.QueueSnapshot{}, err
	}
	files := importedFiles(document)

	queueItem, err := sink.CreateImportedNZB(ctx, database.ImportedNZB{
		FileName:       sanitizeFileName(fileName),
		XML:            raw,
		IdempotencyKey: "manual-nzb:" + hex.EncodeToString(hasher.Sum(nil)),
		FileCount:      len(document.Files),
		SegmentCount:   countSegments(document),
		Files:          files,
		Archives:       archive.DetectImportedArchives(files),
	})
	if err != nil {
		return database.QueueSnapshot{}, err
	}
	if err := sink.SetImportedNZBIndexed(ctx, queueItem.QueueItemID); err != nil {
		return database.QueueSnapshot{}, err
	}
	queueItem.State = database.QueuePreflight
	return queueItem, nil
}

// BuildImportedNZB parses raw NZB XML into a database.ImportedNZB record
// without staging it to disk or persisting it via an ImportSink -- for
// callers (e.g. automated indexer-driven imports) that already have the raw
// bytes plus an idempotency key in hand.
func BuildImportedNZB(fileName string, raw []byte, idempotencyKey string, externalURL string) (database.ImportedNZB, error) {
	document, err := Parse(bytes.NewReader(raw))
	if err != nil {
		return database.ImportedNZB{}, err
	}
	files := importedFiles(document)
	return database.ImportedNZB{
		FileName:       sanitizeFileName(fileName),
		XML:            raw,
		ExternalURL:    externalURL,
		IdempotencyKey: idempotencyKey,
		FileCount:      len(document.Files),
		SegmentCount:   countSegments(document),
		Files:          files,
		Archives:       archive.DetectImportedArchives(files),
	}, nil
}

// sanitizeFileName strips any directory components from name and
// substitutes a default when the result is empty or a bare "." or "/".
func sanitizeFileName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == "/" || name == "" {
		return "imported.nzb"
	}
	return name
}

func countSegments(document *Document) int {
	total := 0
	for _, file := range document.Files {
		total += len(file.Segments)
	}
	return total
}

// importedFiles converts a parsed Document's files into the
// database.ImportedNZBFile records used for persistence, deriving each
// file's total size from its segments' decoded offsets.
func importedFiles(document *Document) []database.ImportedNZBFile {
	out := make([]database.ImportedNZBFile, 0, len(document.Files))
	for _, file := range document.Files {
		entry := database.ImportedNZBFile{
			FileName:      ParseSubjectFilename(file.Subject),
			Subject:       file.Subject,
			Poster:        file.Poster,
			PostedUnix:    file.Date,
			FileSizeBytes: fileSize(file.Segments),
			Segments:      make([]database.ImportedNZBSegment, 0, len(file.Segments)),
		}
		for _, segment := range file.Segments {
			entry.Segments = append(entry.Segments, database.ImportedNZBSegment{
				Number:             segment.Number,
				MessageID:          segment.MessageID,
				EncodedSizeBytes:   segment.Bytes,
				DecodedStartOffset: segment.DecodedFrom,
				DecodedEndOffset:   segment.DecodedTo,
			})
		}
		out = append(out, entry)
	}
	return out
}

// fileSize returns the total decoded file size as the maximum DecodedTo
// offset across segments.
func fileSize(segments []NZBSegment) int64 {
	var end int64
	for _, segment := range segments {
		if segment.DecodedTo > end {
			end = segment.DecodedTo
		}
	}
	return end
}

// ImportHTTPFileName sanitizes a filename supplied via a request header
// (e.g. an upload's filename field), falling back to a default when absent.
func ImportHTTPFileName(headerName string) string {
	headerName = strings.TrimSpace(headerName)
	if headerName == "" {
		return "imported.nzb"
	}
	return sanitizeFileName(headerName)
}

// ImportRawBodyName extracts and sanitizes the filename parameter from a
// Content-Disposition header, falling back to a default when the header has
// no filename.
func ImportRawBodyName(contentDisposition string) string {
	for _, part := range strings.Split(contentDisposition, ";") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(strings.ToLower(part), "filename=") {
			continue
		}
		value := strings.TrimPrefix(part, "filename=")
		return sanitizeFileName(strings.Trim(value, `"`))
	}
	return "imported.nzb"
}

// ValidateUploadLimit returns an error wrapping ErrUploadTooLarge if size
// exceeds limit.
func ValidateUploadLimit(size, limit int64) error {
	if size > limit {
		return fmt.Errorf("%w: got %d bytes limit %d", ErrUploadTooLarge, size, limit)
	}
	return nil
}
