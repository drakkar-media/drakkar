package yenc

import (
	"errors"
	"testing"
)

func TestDecodeArticle(t *testing.T) {
	body := []byte("=ybegin line=128 size=11 name=test.bin\r\n" + encode([]byte("hello world")) + "\r\n=yend size=11\r\n")
	got, err := DecodeArticle(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello world" {
		t.Fatalf("got %q", string(got))
	}
}

func TestDecodeArticleRejectsCRCMismatch(t *testing.T) {
	body := []byte("=ybegin part=1 total=1 line=128 size=11 name=test.bin\r\n=ypart begin=1 end=11\r\n" + encode([]byte("hello world")) + "\r\n=yend size=11 part=1 pcrc32=00000000\r\n")
	_, err := DecodeArticle(body)
	if !errors.Is(err, ErrCRCMismatch) {
		t.Fatalf("got err %v", err)
	}
}

func encode(src []byte) string {
	out := make([]byte, 0, len(src)*2)
	for _, b := range src {
		enc := b + 42
		if enc == 0 || enc == '\n' || enc == '\r' || enc == '=' {
			out = append(out, '=')
			enc += 64
		}
		out = append(out, enc)
	}
	return string(out)
}
