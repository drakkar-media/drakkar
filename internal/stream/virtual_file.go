package stream

import "context"

// VirtualMediaFile is a randomly-readable virtual file backing a stream
// session — a plain in-memory blob, a direct multi-segment NZB, or a file
// embedded in a stored RAR volume. Implementations must support concurrent
// ReadAt calls safely, since one open file can be read by overlapping
// FUSE/HTTP Range requests.
type VirtualMediaFile interface {
	Name() string
	Size() int64
	ReadAt(ctx context.Context, dst []byte, offset int64) (int, error)
}
