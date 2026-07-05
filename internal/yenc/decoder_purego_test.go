package yenc

import "testing"

func TestDecodeArticleWithInfoMatchesSeparateCalls(t *testing.T) {
	body := []byte("=ybegin part=1 total=1 line=128 size=11 name=test.bin\r\n=ypart begin=1 end=11\r\n" + encode([]byte("hello world")) + "\r\n=yend size=11 part=1\r\n")

	wantDecoded, err := DecodeArticle(body)
	if err != nil {
		t.Fatal(err)
	}
	wantInfo, ok := ParsePartInfo(body)
	if !ok {
		t.Fatal("expected ParsePartInfo to find a header")
	}

	gotDecoded, gotInfo, err := DecodeArticleWithInfo(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotDecoded) != string(wantDecoded) {
		t.Fatalf("decoded mismatch: got %q want %q", gotDecoded, wantDecoded)
	}
	if gotInfo != wantInfo {
		t.Fatalf("info mismatch: got %+v want %+v", gotInfo, wantInfo)
	}
}

func TestDecodeArticleWithInfoPropagatesCRCMismatch(t *testing.T) {
	body := []byte("=ybegin part=1 total=1 line=128 size=11 name=test.bin\r\n=ypart begin=1 end=11\r\n" + encode([]byte("hello world")) + "\r\n=yend size=11 part=1 pcrc32=00000000\r\n")
	_, _, err := DecodeArticleWithInfo(body)
	if err == nil {
		t.Fatal("expected CRC mismatch error")
	}
}
