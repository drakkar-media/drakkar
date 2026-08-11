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

// TestDocumentPasswordValueAttrForm covers the <meta type="password"
// value="..."/> form (self-closing, value as an attribute).
func TestDocumentPasswordValueAttrForm(t *testing.T) {
	const nzbWithPasswordAttr = `<?xml version="1.0" encoding="UTF-8"?>
<nzb>
  <head>
    <meta type="category" value="TV"/>
    <meta type="password" value="s3cr3t"/>
  </head>
  <file subject="&quot;show.mkv&quot;" poster="poster" date="1710000000">
    <groups><group>alt.binaries.tv</group></groups>
    <segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments>
  </file>
</nzb>`
	doc, err := Parse(strings.NewReader(nzbWithPasswordAttr))
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Password(); got != "s3cr3t" {
		t.Fatalf("Password() = %q, want %q", got, "s3cr3t")
	}
}

// TestDocumentPasswordChardataForm covers the real-world form confirmed
// live against several actual indexer NZBs: <meta type="password">value</meta>,
// with the value as element character data rather than an attribute.
func TestDocumentPasswordChardataForm(t *testing.T) {
	const nzbWithPasswordChardata = `<?xml version="1.0" encoding="UTF-8"?>
<nzb>
  <head>
    <meta type="category">TV &gt; HD</meta>
    <meta type="name">show.name</meta>
    <meta type="Password">lbITGEvDLs7C7R4WLuaZ</meta>
  </head>
  <file subject="&quot;show.mkv&quot;" poster="poster" date="1710000000">
    <groups><group>alt.binaries.tv</group></groups>
    <segments><segment bytes="1000" number="1">&lt;msg1&gt;</segment></segments>
  </file>
</nzb>`
	doc, err := Parse(strings.NewReader(nzbWithPasswordChardata))
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Password(); got != "lbITGEvDLs7C7R4WLuaZ" {
		t.Fatalf("Password() = %q, want %q", got, "lbITGEvDLs7C7R4WLuaZ")
	}
}

func TestDocumentPasswordAbsent(t *testing.T) {
	doc, err := Parse(strings.NewReader(sampleNZB))
	if err != nil {
		t.Fatal(err)
	}
	if got := doc.Password(); got != "" {
		t.Fatalf("Password() = %q, want empty for an NZB with no <head> at all", got)
	}
}

func TestParseSubjectFilename(t *testing.T) {
	got := ParseSubjectFilename(`"Dune (2021).mkv" yEnc`)
	if got != "Dune (2021).mkv" {
		t.Fatalf("got %s", got)
	}
}
