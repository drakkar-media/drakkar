package stream

import "context"

type VirtualMediaFile interface {
	Name() string
	Size() int64
	ReadAt(ctx context.Context, dst []byte, offset int64) (int, error)
}
