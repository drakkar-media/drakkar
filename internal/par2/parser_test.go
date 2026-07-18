package par2

import (
	"encoding/binary"
	"math"
	"math/rand"
	"testing"
)

// mainType is a non-FileDesc packet type ("PAR 2.0\0Main    ") used to build
// realistic multi-packet PAR2 streams where ParseFileDescs must skip over
// packets it doesn't care about while still advancing past them correctly.
var mainType = [16]byte{'P', 'A', 'R', ' ', '2', '.', '0', 0, 'M', 'a', 'i', 'n', ' ', ' ', ' ', ' '}

// buildHeader returns the 64-byte packet header (magic, length, MD5, SetID,
// type) for a packet of the given total length and type. MD5/SetID are left
// zeroed since ParseFileDescs never validates them.
func buildHeader(totalLen int, pktType [16]byte) []byte {
	buf := make([]byte, 64)
	copy(buf[0:8], packetMagic[:])
	binary.LittleEndian.PutUint64(buf[8:16], uint64(totalLen))
	copy(buf[48:64], pktType[:])
	return buf
}

// buildFileDescBody constructs the body of a FileDesc packet (bytes
// 64..Length) per the layout documented at the top of parser.go:
// [0:16] FileID, [16:32] FileHash, [32:48] File16kHash, [48:56] FileLength,
// [56:] FileName padded to a 4-byte boundary.
func buildFileDescBody(fileIDByte byte, fileLength uint64, fileName string) []byte {
	name := []byte(fileName)
	padded := len(name)
	if rem := padded % 4; rem != 0 {
		padded += 4 - rem
	}
	body := make([]byte, 56+padded)
	for i := 0; i < 16; i++ {
		body[i] = fileIDByte
	}
	binary.LittleEndian.PutUint64(body[48:56], fileLength)
	copy(body[56:], name)
	return body
}

// buildFileDescPacket returns a complete, well-formed FileDesc packet.
func buildFileDescPacket(fileIDByte byte, fileLength uint64, fileName string) []byte {
	body := buildFileDescBody(fileIDByte, fileLength, fileName)
	total := 64 + len(body)
	return append(buildHeader(total, fileDescType), body...)
}

func TestParseFileDescsSingleWellFormedPacket(t *testing.T) {
	data := buildFileDescPacket(0xAB, 123456, "Show.S01E01.mkv")

	got := ParseFileDescs(data)
	if len(got) != 1 {
		t.Fatalf("expected 1 FileDesc, got %d", len(got))
	}
	if got[0].FileName != "Show.S01E01.mkv" {
		t.Errorf("FileName = %q, want %q", got[0].FileName, "Show.S01E01.mkv")
	}
	if got[0].FileLength != 123456 {
		t.Errorf("FileLength = %d, want 123456", got[0].FileLength)
	}
	var wantID [16]byte
	for i := range wantID {
		wantID[i] = 0xAB
	}
	if got[0].FileID != wantID {
		t.Errorf("FileID = %x, want %x", got[0].FileID, wantID)
	}
}

// TestParseFileDescsMultiplePacketsAdvanceCorrectly builds a realistic
// mixed-packet stream (FileDesc, a non-FileDesc "Main" packet, another
// FileDesc) and checks that the scanner advances packet-to-packet by the
// declared length rather than losing sync, correctly skipping the packet
// type it doesn't care about while still finding both FileDescs.
func TestParseFileDescsMultiplePacketsAdvanceCorrectly(t *testing.T) {
	var data []byte
	data = append(data, buildFileDescPacket(0x01, 111, "a.mkv")...)
	data = append(data, buildHeader(64, mainType)...) // zero-body Main packet
	data = append(data, buildFileDescPacket(0x02, 222, "b.mkv")...)

	got := ParseFileDescs(data)
	if len(got) != 2 {
		t.Fatalf("expected 2 FileDescs, got %d", len(got))
	}
	if got[0].FileName != "a.mkv" || got[0].FileLength != 111 {
		t.Errorf("first FileDesc = %+v, want FileName=a.mkv FileLength=111", got[0])
	}
	if got[1].FileName != "b.mkv" || got[1].FileLength != 222 {
		t.Errorf("second FileDesc = %+v, want FileName=b.mkv FileLength=222", got[1])
	}
}

// TestParseFileDescsSkipsLeadingGarbageByteByByte ensures the scanner
// recovers sync when the magic bytes don't start at offset 0 -- it must
// advance one byte at a time (not the packet-length stride) until it finds a
// real magic match, and then correctly parse the packet that follows.
func TestParseFileDescsSkipsLeadingGarbageByteByByte(t *testing.T) {
	garbage := []byte("this is not a par2 packet, just junk bytes before the real one")
	data := append(garbage, buildFileDescPacket(0x03, 999, "c.mkv")...)

	got := ParseFileDescs(data)
	if len(got) != 1 {
		t.Fatalf("expected 1 FileDesc after skipping leading garbage, got %d", len(got))
	}
	if got[0].FileName != "c.mkv" || got[0].FileLength != 999 {
		t.Errorf("FileDesc = %+v, want FileName=c.mkv FileLength=999", got[0])
	}
}

// TestParseFileDescsEmptyInput must return no results and must not panic.
func TestParseFileDescsEmptyInput(t *testing.T) {
	if got := ParseFileDescs(nil); len(got) != 0 {
		t.Fatalf("expected 0 FileDescs for nil input, got %d", len(got))
	}
	if got := ParseFileDescs([]byte{}); len(got) != 0 {
		t.Fatalf("expected 0 FileDescs for empty input, got %d", len(got))
	}
}

// TestParseFileDescsShorterThanHeader covers data too short to hold even one
// 64-byte header -- the "i+64 <= len(data)" loop bound must prevent any
// out-of-bounds slice access.
func TestParseFileDescsShorterThanHeader(t *testing.T) {
	data := buildFileDescPacket(0x04, 1, "x")[:40] // truncated mid-header
	got := ParseFileDescs(data)
	if len(got) != 0 {
		t.Fatalf("expected 0 FileDescs from a truncated header, got %d", len(got))
	}
}

// TestParseFileDescsPktLenTooSmall covers a length field smaller than the
// 64-byte header itself (impossible for a real packet) -- must be rejected
// via the "pktLen < 64" guard and the scan must still advance (by 1 byte)
// rather than looping forever on the same offset.
func TestParseFileDescsPktLenTooSmall(t *testing.T) {
	data := buildFileDescPacket(0x05, 1, "x")
	binary.LittleEndian.PutUint64(data[8:16], 10) // claim a 10-byte packet

	got := ParseFileDescs(data)
	if len(got) != 0 {
		t.Fatalf("expected 0 FileDescs for an undersized length field, got %d", len(got))
	}
}

// TestParseFileDescsPktLenExceedsAvailableData is the core truncated-packet
// case: the header declares a length longer than the actual remaining data
// (e.g. the file was cut off mid-download/mid-write). Must be silently
// skipped, not panic on an out-of-range slice.
func TestParseFileDescsPktLenExceedsAvailableData(t *testing.T) {
	full := buildFileDescPacket(0x06, 42, "truncated.mkv")
	truncated := full[:len(full)-10] // header still claims the full length

	got := ParseFileDescs(truncated)
	if len(got) != 0 {
		t.Fatalf("expected 0 FileDescs when declared length exceeds available data, got %d", len(got))
	}
}

// TestParseFileDescsPktLenIsHugeMaliciousValue guards the classic
// hand-rolled-binary-parser trap: a corrupt/adversarial length field set to
// the maximum uint64 value. int(uint64) reinterprets the bits, so this
// becomes a large negative int, which the "pktLen < 64" check must catch --
// if it didn't, "i+pktLen > len(data)" or a subsequent slice expression could
// wrap/overflow and panic instead of gracefully skipping the packet.
func TestParseFileDescsPktLenIsHugeMaliciousValue(t *testing.T) {
	data := buildFileDescPacket(0x07, 1, "x")
	binary.LittleEndian.PutUint64(data[8:16], math.MaxUint64)

	got := ParseFileDescs(data) // must not panic
	if len(got) != 0 {
		t.Fatalf("expected 0 FileDescs for a maliciously huge length field, got %d", len(got))
	}
}

// TestParseFileDescsBodyShorterThan56Bytes covers a packet whose declared
// length fits within the available data (so the outer bounds check passes)
// but whose body is shorter than the fixed 56-byte FileDesc prefix -- must be
// rejected by parseFileDescBody's own length check rather than panicking on
// the FileLength/FileID slice reads.
func TestParseFileDescsBodyShorterThan56Bytes(t *testing.T) {
	body := make([]byte, 20) // shorter than the 56-byte fixed FileDesc prefix
	total := 64 + len(body)
	data := append(buildHeader(total, fileDescType), body...)

	got := ParseFileDescs(data)
	if len(got) != 0 {
		t.Fatalf("expected 0 FileDescs for an undersized FileDesc body, got %d", len(got))
	}
}

// TestParseFileDescsZeroLengthFileIsSkipped documents existing behavior:
// parseFileDescBody only reports ok=true when FileLength > 0, so a
// (well-formed, but declaring a zero-byte file) FileDesc packet is silently
// dropped rather than returned as a zero-length entry.
func TestParseFileDescsZeroLengthFileIsSkipped(t *testing.T) {
	data := buildFileDescPacket(0x08, 0, "empty.mkv")
	got := ParseFileDescs(data)
	if len(got) != 0 {
		t.Fatalf("expected a zero FileLength FileDesc to be skipped, got %d entries", len(got))
	}
}

// TestParseFileDescsFileNameWithoutNullTerminator covers a filename that
// fills the entire remaining body with no null terminator at all -- the
// null-byte scan must stop at len(raw) rather than reading past the body.
func TestParseFileDescsFileNameWithoutNullTerminator(t *testing.T) {
	body := make([]byte, 56+8)
	binary.LittleEndian.PutUint64(body[48:56], 55)
	copy(body[56:], "nozero!!") // exactly fills the remaining 8 bytes, no \0
	total := 64 + len(body)
	data := append(buildHeader(total, fileDescType), body...)

	got := ParseFileDescs(data)
	if len(got) != 1 {
		t.Fatalf("expected 1 FileDesc, got %d", len(got))
	}
	if got[0].FileName != "nozero!!" {
		t.Errorf("FileName = %q, want %q", got[0].FileName, "nozero!!")
	}
}

// TestParseFileDescsBodyExactly56Bytes covers a FileDesc body with no
// filename bytes at all (body ends exactly at the fixed 56-byte prefix) --
// FileName must come back empty, not panic on an out-of-range slice.
func TestParseFileDescsBodyExactly56Bytes(t *testing.T) {
	body := make([]byte, 56)
	binary.LittleEndian.PutUint64(body[48:56], 77)
	total := 64 + len(body)
	data := append(buildHeader(total, fileDescType), body...)

	got := ParseFileDescs(data)
	if len(got) != 1 {
		t.Fatalf("expected 1 FileDesc, got %d", len(got))
	}
	if got[0].FileName != "" {
		t.Errorf("FileName = %q, want empty", got[0].FileName)
	}
	if got[0].FileLength != 77 {
		t.Errorf("FileLength = %d, want 77", got[0].FileLength)
	}
}

// TestParseFileDescsFuzzRandomBytesNeverPanics stress-tests the bounds-check
// safety of the hand-rolled scanner: random byte buffers of varying lengths
// (including ones that happen to contain the magic bytes at random offsets)
// must never panic or hang, regardless of what garbage length/type fields
// they produce.
func TestParseFileDescsFuzzRandomBytesNeverPanics(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for trial := 0; trial < 200; trial++ {
		n := rng.Intn(300)
		buf := make([]byte, n)
		rng.Read(buf)
		// Occasionally splice in the magic bytes at a random offset to
		// increase the odds of exercising the length/type parsing path.
		if n > 8 && rng.Intn(3) == 0 {
			offset := rng.Intn(n - 8)
			copy(buf[offset:offset+8], packetMagic[:])
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("ParseFileDescs panicked on random input (trial %d, len %d): %v", trial, n, r)
				}
			}()
			ParseFileDescs(buf)
		}()
	}
}
