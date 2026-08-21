package database

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"testing"

	"github.com/drakkar-media/drakkar/internal/rarcrypto"
)

// computePasswordCheck independently re-derives RAR5's 8-byte password-check
// value for (password, salt, lg2Count) -- a duplicate of the private
// computation rarcrypto.DeriveKey does internally, kept here (rather than
// exported from rarcrypto) since computing a check value from scratch is
// only ever needed to BUILD a test fixture; real production code only ever
// verifies against an already-stored one.
func computePasswordCheck(password string, salt [16]byte, lg2Count uint8) [8]byte {
	saltExtended := append(append([]byte{}, salt[:]...), 0, 0, 0, 1)
	iterations := uint32(1) << lg2Count
	roundCounts := [3]uint32{iterations, 17, 17}

	pw := []byte(password)
	mac := hmac.New(sha256.New, pw)
	mac.Write(saltExtended)
	block := mac.Sum(nil)
	var finalHash [32]byte
	copy(finalHash[:], block)

	var results [3][32]byte
	for round, count := range roundCounts {
		for i := uint32(1); i < count; i++ {
			mac := hmac.New(sha256.New, pw)
			mac.Write(block)
			block = mac.Sum(nil)
			for j := 0; j < 32; j++ {
				finalHash[j] ^= block[j]
			}
		}
		results[round] = finalHash
	}
	var check [8]byte
	for i, b := range results[2] {
		check[i%8] ^= b
	}
	return check
}

// rar5WriteVint appends v encoded as a RAR5 vint (7 bits per byte,
// little-endian, high bit set on every byte but the last).
func rar5WriteVint(v int64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			out = append(out, b|0x80)
		} else {
			out = append(out, b)
			break
		}
	}
	return out
}

// rar5Header wraps body (everything after headFlags -- type-specific fields
// then extra area, in that order, matching how inspectRAR5 slices
// [pos:typeEnd] vs [typeEnd:bodyEnd]) into a full RAR5 header block: 4 bytes
// CRC32 (never validated by inspectRAR5, zero is fine), a headerSize vint,
// then headType/headFlags/optional extraAreaSize/optional dataAreaSize
// vints, then body itself. extraAreaSize must equal len(extra) exactly for
// typeEnd to land where the caller intends.
func rar5Header(headType int64, extra []byte, typeSpecific []byte, dataAreaSize int64) []byte {
	var headFlags int64
	if len(extra) > 0 {
		headFlags |= 0x0001
	}
	if dataAreaSize > 0 {
		headFlags |= 0x0002
	}
	body := append(rar5WriteVint(headType), rar5WriteVint(headFlags)...)
	if len(extra) > 0 {
		body = append(body, rar5WriteVint(int64(len(extra)))...)
	}
	if dataAreaSize > 0 {
		body = append(body, rar5WriteVint(dataAreaSize)...)
	}
	body = append(body, typeSpecific...)
	body = append(body, extra...)

	out := make([]byte, 4) // CRC32, unchecked
	out = append(out, rar5WriteVint(int64(len(body)))...)
	out = append(out, body...)
	return out
}

// rar5EncryptionHeaderBody builds a type-4 encryption header's body
// (version=0, flags=has-password-check, lg2Count, salt, psw_check+checksum)
// -- the exact layout parseRAR5EncryptionHeaderBody expects.
func rar5EncryptionHeaderBody(t *testing.T, password string, salt [16]byte, lg2Count uint8) []byte {
	t.Helper()
	params := rarcrypto.EncryptionParams{Lg2Count: lg2Count, Salt: salt}
	key, err := rarcrypto.DeriveKey(password, params)
	if err != nil {
		t.Fatalf("DeriveKey (no check): %v", err)
	}
	_ = key
	// Compute the real password-check value the same way DeriveKey
	// verifies it, so a correct password round-trips through
	// inspectRAR5 exactly like a real archive's would.
	check := computePasswordCheck(password, salt, lg2Count)

	body := []byte{0}      // version
	body = append(body, 1) // flags: has_psw_check
	body = append(body, lg2Count)
	body = append(body, salt[:]...)
	body = append(body, check[:]...)
	body = append(body, []byte{0, 0, 0, 0}...) // checksum, unchecked by our parser
	return body
}

// rar5FileEncryptionExtraRecord builds a file header's extra-area
// encryption record (extra type 1): esize vint, etype vint, version, flags,
// lg2Count, salt, iv, and (since flags sets has_psw_check) an 8-byte check
// + 4-byte checksum.
func rar5FileEncryptionExtraRecord(password string, salt, iv [16]byte, lg2Count uint8) []byte {
	check := computePasswordCheck(password, salt, lg2Count)
	record := rar5WriteVint(1) // etype = encryption
	record = append(record, 0) // version
	record = append(record, 1) // flags: has_psw_check
	record = append(record, lg2Count)
	record = append(record, salt[:]...)
	record = append(record, iv[:]...)
	record = append(record, check[:]...)
	record = append(record, []byte{0, 0, 0, 0}...) // checksum, unchecked
	out := rar5WriteVint(int64(len(record)))
	out = append(out, record...)
	return out
}

// rar5FileEncryptionExtraRecordNoCheck is rar5FileEncryptionExtraRecord's
// twin for a file whose own encryption record carries no password-check
// value at all (flags=0, no 8-byte check/4-byte checksum trailer) -- a real,
// if unusual, RAR5 layout DeriveKey can't verify a password against by
// design, matching the archive_inspect.go:1102 finding.
func rar5FileEncryptionExtraRecordNoCheck(salt, iv [16]byte, lg2Count uint8) []byte {
	record := rar5WriteVint(1) // etype = encryption
	record = append(record, 0) // version
	record = append(record, 0) // flags: no psw_check
	record = append(record, lg2Count)
	record = append(record, salt[:]...)
	record = append(record, iv[:]...)
	out := rar5WriteVint(int64(len(record)))
	out = append(out, record...)
	return out
}

// rar5FileHeaderTypeSpecific builds a file header's fixed fields up through
// the filename, matching parseRAR5FileHeader's expected field order
// exactly: fileFlags(size known, no mtime/crc), unpackedSize, attributes,
// compressionInfo (method=store=0, not solid), hostOS, namelen+name.
func rar5FileHeaderTypeSpecific(name string, unpackedSize int64) []byte {
	out := rar5WriteVint(0) // fileFlags: size known, no optional mtime/crc
	out = append(out, rar5WriteVint(unpackedSize)...)
	out = append(out, rar5WriteVint(0)...) // attributes
	out = append(out, rar5WriteVint(0)...) // compressionInfo: method=store, not solid
	out = append(out, rar5WriteVint(0)...) // host OS
	out = append(out, rar5WriteVint(int64(len(name)))...)
	out = append(out, []byte(name)...)
	return out
}

// buildRAR5EncryptedArchive assembles a minimal, single-volume RAR5 archive
// whose headers are encrypted (type-4 encryption header) and whose one file
// entry additionally carries its own file-level encryption record (extra
// type 1), with fileContentPlaintext encrypted under that file's own
// key/IV. This exercises the exact real-world layout confirmed byte-for-byte
// against a genuine production archive earlier this session: same
// archive-header salt/psw_check reused as the file's own encryption
// parameters, file IV explicit and distinct.
func buildRAR5EncryptedArchive(t *testing.T, password, fileName string, fileContentPlaintext []byte, fileHasPasswordCheck bool) []byte {
	t.Helper()
	if len(fileContentPlaintext)%rarcrypto.BlockSize != 0 {
		t.Fatalf("fixture content must be block-aligned, got %d bytes", len(fileContentPlaintext))
	}

	var salt, fileIV [16]byte
	for i := range salt {
		salt[i] = byte(0x10 + i)
	}
	for i := range fileIV {
		fileIV[i] = byte(0x80 + i)
	}
	const lg2Count = 4 // tiny iteration count -- keeps the test fast; the algorithm doesn't care

	archiveHeader := rar5Header(1, nil, rar5WriteVint(0), 0) // archive header, no flags, no extra area

	var fileExtra []byte
	if fileHasPasswordCheck {
		fileExtra = rar5FileEncryptionExtraRecord(password, salt, fileIV, lg2Count)
	} else {
		fileExtra = rar5FileEncryptionExtraRecordNoCheck(salt, fileIV, lg2Count)
	}
	fileTypeSpecific := rar5FileHeaderTypeSpecific(fileName, int64(len(fileContentPlaintext)))
	fileHeaderPlain := rar5Header(2, fileExtra, fileTypeSpecific, int64(len(fileContentPlaintext)))

	// Pad the plaintext header region to a block boundary -- real RAR5
	// pads the encrypted-headers region similarly; inspectRAR5 only
	// decrypts whole blocks and stops parsing once it runs out of
	// recognizable headers, so trailing padding is harmless.
	headerPlaintext := fileHeaderPlain
	if pad := len(headerPlaintext) % rarcrypto.BlockSize; pad != 0 {
		headerPlaintext = append(headerPlaintext, make([]byte, rarcrypto.BlockSize-pad)...)
	}

	headerKey, err := rarcrypto.DeriveKey(password, rarcrypto.EncryptionParams{Lg2Count: lg2Count, Salt: salt})
	if err != nil {
		t.Fatalf("DeriveKey: %v", err)
	}
	var headerIV [16]byte
	for i := range headerIV {
		headerIV[i] = byte(0xC0 + i)
	}
	block, err := aes.NewCipher(headerKey[:])
	if err != nil {
		t.Fatal(err)
	}
	encryptedHeaders := make([]byte, len(headerPlaintext))
	cipher.NewCBCEncrypter(block, headerIV[:]).CryptBlocks(encryptedHeaders, headerPlaintext)

	encHeaderBody := rar5EncryptionHeaderBody(t, password, salt, lg2Count)
	encryptionHeader := rar5Header(4, nil, encHeaderBody, 0)

	fileKey, err := rarcrypto.DeriveKey(password, rarcrypto.EncryptionParams{Lg2Count: lg2Count, Salt: salt})
	if err != nil {
		t.Fatal(err)
	}
	fileBlock, err := aes.NewCipher(fileKey[:])
	if err != nil {
		t.Fatal(err)
	}
	encryptedContent := make([]byte, len(fileContentPlaintext))
	cipher.NewCBCEncrypter(fileBlock, fileIV[:]).CryptBlocks(encryptedContent, fileContentPlaintext)

	var out []byte
	out = append(out, []byte("Rar!\x1a\x07\x01\x00")...)
	out = append(out, archiveHeader...)
	out = append(out, encryptionHeader...)
	out = append(out, headerIV[:]...)
	out = append(out, encryptedHeaders...)
	out = append(out, encryptedContent...)
	return out
}

func TestInspectRAR5DecryptsHeadersAndAcceptsEncryptedStoredEntryWithCorrectPassword(t *testing.T) {
	plaintext := bytes.Repeat([]byte("VIDEO-CONTENT-16"), 4) // 64 bytes, block-aligned
	raw := buildRAR5EncryptedArchive(t, "correct-password", "Movie.mkv", plaintext, true)

	entries, err := inspectRAR5(raw, "correct-password")
	if err != nil {
		t.Fatalf("expected success with the correct password, got: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one entry, got %+v", entries)
	}
	entry := entries[0]
	if entry.Path != "Movie.mkv" {
		t.Fatalf("Path = %q, want Movie.mkv", entry.Path)
	}
	if !entry.Encrypted || !entry.EncryptionVerified {
		t.Fatalf("expected Encrypted=true and EncryptionVerified=true, got %+v", entry)
	}
	if entry.CompressionMethod != "m0" {
		t.Fatalf("CompressionMethod = %q, want m0", entry.CompressionMethod)
	}
}

func TestInspectRAR5RejectsEncryptedArchiveWithWrongPassword(t *testing.T) {
	plaintext := bytes.Repeat([]byte("VIDEO-CONTENT-16"), 4)
	raw := buildRAR5EncryptedArchive(t, "correct-password", "Movie.mkv", plaintext, true)

	_, err := inspectRAR5(raw, "wrong-password")
	if err != errArchiveEncrypted {
		t.Fatalf("expected errArchiveEncrypted for a wrong password, got: %v", err)
	}
}

// buildRAR5FileEncryptedOnlyArchive assembles a RAR5 archive with PLAINTEXT
// headers (no type-4 encryption header at all -- the common "encrypt files
// only, not names" configuration) whose one file entry carries its own
// file-level encryption record with no password-check value. This is the
// only layout where a caller can reach parseRAR5FileHeader's per-file
// DeriveKey call without the archive password already having been verified
// by header decryption first -- exactly the real-world case
// archive_inspect.go:1102 covers.
func buildRAR5FileEncryptedOnlyArchive(fileName string, fileContentPlaintext []byte) []byte {
	var salt, fileIV [16]byte
	for i := range salt {
		salt[i] = byte(0x20 + i)
	}
	for i := range fileIV {
		fileIV[i] = byte(0x90 + i)
	}
	const lg2Count = 4

	archiveHeader := rar5Header(1, nil, rar5WriteVint(0), 0)
	fileExtra := rar5FileEncryptionExtraRecordNoCheck(salt, fileIV, lg2Count)
	fileTypeSpecific := rar5FileHeaderTypeSpecific(fileName, int64(len(fileContentPlaintext)))
	fileHeader := rar5Header(2, fileExtra, fileTypeSpecific, int64(len(fileContentPlaintext)))

	var out []byte
	out = append(out, []byte("Rar!\x1a\x07\x01\x00")...)
	out = append(out, archiveHeader...)
	out = append(out, fileHeader...)
	// Content isn't actually encrypted correctly under any particular
	// password here -- irrelevant to this test, which only checks whether
	// EncryptionVerified gets wrongly rubber-stamped before content is ever
	// read.
	out = append(out, fileContentPlaintext...)
	return out
}

// TestInspectRAR5RejectsFileEncryptedOnlyArchiveWithNoPasswordCheckValue
// guards the exact bug described at archive_inspect.go:1102: when a file's
// own encryption record has no password-check value, DeriveKey cannot fail
// regardless of whether the supplied password is right -- so a bare "no
// error" must NOT be trusted as proof of a correct password, or a wrong
// password gets rubber-stamped EncryptionVerified=true and the archive is
// accepted for import with content that will decrypt to garbage.
func TestInspectRAR5RejectsFileEncryptedOnlyArchiveWithNoPasswordCheckValue(t *testing.T) {
	plaintext := bytes.Repeat([]byte("VIDEO-CONTENT-16"), 4)
	raw := buildRAR5FileEncryptedOnlyArchive("Movie.mkv", plaintext)

	_, err := inspectRAR5(raw, "some-password-that-may-or-may-not-be-right")
	if err != errArchiveEncrypted {
		t.Fatalf("expected errArchiveEncrypted when the file's encryption record has no password-check value, got: %v", err)
	}
}

func TestInspectRAR5RejectsEncryptedArchiveWithNoPassword(t *testing.T) {
	plaintext := bytes.Repeat([]byte("VIDEO-CONTENT-16"), 4)
	raw := buildRAR5EncryptedArchive(t, "correct-password", "Movie.mkv", plaintext, true)

	_, err := inspectRAR5(raw, "")
	if err != errArchiveEncrypted {
		t.Fatalf("expected errArchiveEncrypted with no password at all, got: %v", err)
	}
}

// TestInspectRAR5EncryptedContentDecryptsCorrectlyViaEncryptedRarReader is
// the full end-to-end proof: given the salt/IV inspectRAR5 extracted from a
// real (synthetic but structurally faithful) encrypted archive, actually
// decrypting the file's content through stream.EncryptedRarReader-equivalent
// logic (rarcrypto directly here, since that's all the reader wraps)
// recovers the exact original plaintext.
func TestInspectRAR5EncryptedContentDecryptsCorrectlyViaEncryptedRarReader(t *testing.T) {
	plaintext := bytes.Repeat([]byte("VIDEO-CONTENT-16"), 4)
	raw := buildRAR5EncryptedArchive(t, "correct-password", "Movie.mkv", plaintext, true)

	entries, err := inspectRAR5(raw, "correct-password")
	if err != nil {
		t.Fatalf("inspectRAR5: %v", err)
	}
	entry := entries[0]

	key, err := rarcrypto.DeriveKey("correct-password", rarcrypto.EncryptionParams{
		Lg2Count: entry.EncryptionLg2Count,
		Salt:     entry.EncryptionSalt,
	})
	if err != nil {
		t.Fatalf("DeriveKey at read time: %v", err)
	}
	ciphertext := raw[entry.ArchiveOffset : entry.ArchiveOffset+int64(len(plaintext))]
	decrypted, err := rarcrypto.DecryptBlockAligned(ciphertext, key, entry.EncryptionIV)
	if err != nil {
		t.Fatalf("DecryptBlockAligned: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted content = %q, want %q", decrypted, plaintext)
	}
}
