package stream

import (
	"context"
	"io"
)

type ByteVirtualFile struct {
	name string
	data []byte
}

func NewByteVirtualFile(name string, data []byte) *ByteVirtualFile {
	clone := make([]byte, len(data))
	copy(clone, data)
	return &ByteVirtualFile{name: name, data: clone}
}

func (f *ByteVirtualFile) Name() string {
	return f.name
}

func (f *ByteVirtualFile) Size() int64 {
	return int64(len(f.data))
}

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
