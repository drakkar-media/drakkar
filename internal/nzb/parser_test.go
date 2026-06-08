package nzb

import (
	"strings"
	"testing"
)

const sampleNZB = `<?xml version="1.0" encoding="UTF-8"?>
<nzb>
  <file subject="&quot;Dune (2021).mkv&quot;" poster="poster" date="1710000000">
    <groups><group>alt.binaries.movies</group></groups>
    <segments>
      <segment bytes="1000" number="2">&lt;msg2&gt;</segment>
      <segment bytes="1000" number="1">&lt;msg1&gt;</segment>
    </segments>
  </file>
</nzb>`

func TestParseNZB(t *testing.T) {
	doc, err := Parse(strings.NewReader(sampleNZB))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(doc.Files))
	}
	if doc.Files[0].Segments[0].Number != 1 {
		t.Fatalf("segments not sorted: %+v", doc.Files[0].Segments)
	}
}

func TestParseSubjectFilename(t *testing.T) {
	got := ParseSubjectFilename(`"Dune (2021).mkv" yEnc`)
	if got != "Dune (2021).mkv" {
		t.Fatalf("got %s", got)
	}
}
