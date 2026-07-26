package stream

import (
	"context"
	"io"
)

// ByteVirtualFile is a VirtualMediaFile backed by an in-memory byte slice,
// used for small generated or cached files rather than NZB-backed streams.
type ByteVirtualFile struct {
	name string
	data []byte
}

// NewByteVirtualFile creates a ByteVirtualFile from a clone of data, so later
// mutation of the caller's slice cannot affect the stored contents.
func NewByteVirtualFile(name string, data []byte) *ByteVirtualFile {
	clone := make([]byte, len(data))
	copy(clone, data)
	return &ByteVirtualFile{name: name, data: clone}
}

// Name returns the file's display name.
func (f *ByteVirtualFile) Name() string {
	return f.name
}

// Size returns the length of the backing byte slice.
func (f *ByteVirtualFile) Size() int64 {
	return int64(len(f.data))
}

// ReadAt copies from the immutable backing buffer starting at offset; safe
// for concurrent calls since data is never modified after construction.
func (f *ByteVirtualFile) ReadAt(ctx context.Context, dst []byte, offset int64) (int, error) {
	if offset >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(dst, f.data[offset:])
	if n < len(dst) {
		return n, io.EOF
	}
	return n, nil
}
