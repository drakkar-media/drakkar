package yenc

import "testing"

func TestDecodeArticle(t *testing.T) {
	body := []byte("=ybegin line=128 size=11 name=test.bin\r\n" + encode([]byte("hello world")) + "\r\n=yend size=11 crc32=00000000\r\n")
	got, err := DecodeArticle(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello world" {
		t.Fatalf("got %q", string(got))
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
