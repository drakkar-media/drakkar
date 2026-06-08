package stream

import (
	"context"
	"io"
	"testing"
)

func TestByteVirtualFileReadAt(t *testing.T) {
	file := NewByteVirtualFile("test.bin", []byte("hello world"))
	buf := make([]byte, 5)
	n, err := file.ReadAt(context.Background(), buf, 6)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if n != 5 || string(buf) != "world" {
		t.Fatalf("got n=%d buf=%q", n, string(buf))
	}
}
