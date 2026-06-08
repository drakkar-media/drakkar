package nntp

import (
	"bufio"
	"strings"
	"testing"
)

func TestReadStatusLine(t *testing.T) {
	code, text, err := readStatusLine(bufio.NewReader(strings.NewReader("222 0 <msg> body follows\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if code != 222 || text != "0 <msg> body follows" {
		t.Fatalf("got %d %q", code, text)
	}
}

func TestReadMultilineBody(t *testing.T) {
	body, err := readMultilineBody(bufio.NewReader(strings.NewReader("line1\r\n..line2\r\n.\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "line1\r\n.line2\r\n" {
		t.Fatalf("got %q", string(body))
	}
}

func TestNormalizeMessageID(t *testing.T) {
	tests := map[string]string{
		"msg@example":         "<msg@example>",
		"<msg@example>":       "<msg@example>",
		" <msg@example> \r\n": "<msg@example>",
	}
	for input, want := range tests {
		if got := normalizeMessageID(input); got != want {
			t.Fatalf("normalizeMessageID(%q) = %q want %q", input, got, want)
		}
	}
}
