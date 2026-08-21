package stream

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"io"
	"testing"

	"github.com/drakkar-media/drakkar/internal/rarcrypto"
)

// buildEncryptedFixture builds a valid MKV-magic-prefixed plaintext of size
// n bytes (block-aligned), encrypts it with key/iv via a standard-library
// AES-256-CBC encryptor (independent of the code under test), and returns
// both the plaintext and an inner VirtualMediaFile serving the ciphertext --
// exactly what EncryptedRarReader expects to wrap.
func buildEncryptedFixture(t *testing.T, key [rarcrypto.KeySize]byte, iv [rarcrypto.BlockSize]byte, n int) (plaintext []byte, inner VirtualMediaFile) {
	t.Helper()
	if n%rarcrypto.BlockSize != 0 {
		t.Fatalf("fixture size must be block-aligned, got %d", n)
	}
	plaintext = make([]byte, n)
	// Real MKV EBML magic at the very start -- what VerifyContainerHeader checks.
	copy(plaintext, []byte{0x1a, 0x45, 0xdf, 0xa3})
	for i := 4; i < n; i++ {
		plaintext[i] = byte(i)
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := make([]byte, n)
	cipher.NewCBCEncrypter(block, iv[:]).CryptBlocks(ciphertext, plaintext)
	return plaintext, NewByteVirtualFile("encrypted.mkv", ciphertext)
}

func testKeyIV() (key [rarcrypto.KeySize]byte, iv [rarcrypto.BlockSize]byte) {
	for i := range key {
		key[i] = byte(i * 7)
	}
	for i := range iv {
		iv[i] = byte(0x10 + i)
	}
	return key, iv
}

func TestEncryptedRarReaderDecryptsFromOffsetZero(t *testing.T) {
	key, iv := testKeyIV()
	plaintext, inner := buildEncryptedFixture(t, key, iv, rarcrypto.BlockSize*4)
	r := NewEncryptedRarReader(inner, key, iv)

	dst := make([]byte, len(plaintext))
	n, err := r.ReadAt(context.Background(), dst, 0)
	if err != nil && err != io.EOF {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != len(plaintext) || !bytes.Equal(dst, plaintext) {
		t.Fatalf("decrypted mismatch: got %v, want %v", dst[:n], plaintext)
	}
}

// TestEncryptedRarReaderDecryptsArbitraryMidFileOffset is the key
// random-access property: reading a range that starts mid-file (not
// block-aligned, and not from offset 0) must still decrypt correctly, using
// only the preceding ciphertext block as chain input -- never a full
// decrypt-from-the-start pass.
func TestEncryptedRarReaderDecryptsArbitraryMidFileOffset(t *testing.T) {
	key, iv := testKeyIV()
	plaintext, inner := buildEncryptedFixture(t, key, iv, rarcrypto.BlockSize*8)
	r := NewEncryptedRarReader(inner, key, iv)

	// Deliberately non-block-aligned offset, well past the start.
	offset := int64(rarcrypto.BlockSize*3 + 5)
	length := 20
	dst := make([]byte, length)
	n, err := r.ReadAt(context.Background(), dst, offset)
	if err != nil && err != io.EOF {
		t.Fatalf("unexpected error: %v", err)
	}
	want := plaintext[offset : offset+int64(length)]
	if n != length || !bytes.Equal(dst, want) {
		t.Fatalf("mid-file decrypt mismatch: got %v, want %v", dst[:n], want)
	}
}

func TestEncryptedRarReaderReturnsEOFAtEndOfFile(t *testing.T) {
	key, iv := testKeyIV()
	plaintext, inner := buildEncryptedFixture(t, key, iv, rarcrypto.BlockSize*2)
	r := NewEncryptedRarReader(inner, key, iv)

	dst := make([]byte, 10)
	n, err := r.ReadAt(context.Background(), dst, int64(len(plaintext)-5))
	if err != io.EOF {
		t.Fatalf("expected io.EOF for a read past the end, got %v", err)
	}
	if n != 5 || !bytes.Equal(dst[:5], plaintext[len(plaintext)-5:]) {
		t.Fatalf("expected the last 5 real bytes before EOF, got %v (n=%d)", dst[:n], n)
	}
}

// TestEncryptedRarReaderRejectsWrongKey is the actual safety-net test: a
// wrong key must NOT silently return garbage as if it were valid video --
// VerifyContainerHeader must catch it and ReadAt must fail instead of
// returning corrupted bytes to whatever's asking (a real player, in
// production).
func TestEncryptedRarReaderRejectsWrongKey(t *testing.T) {
	key, iv := testKeyIV()
	_, inner := buildEncryptedFixture(t, key, iv, rarcrypto.BlockSize*4)

	wrongKey := key
	wrongKey[0] ^= 0xFF
	r := NewEncryptedRarReader(inner, wrongKey, iv)

	dst := make([]byte, rarcrypto.BlockSize)
	_, err := r.ReadAt(context.Background(), dst, 0)
	if err == nil {
		t.Fatal("expected an error when decrypting with the wrong key, got success")
	}
}

// TestEncryptedRarReaderRejectsWrongKeyWhenFirstReadIsNotAtOffsetZero is the
// direct regression test for the actual gap confirmed live 2026-08-20:
// verification previously only ran as a side effect of a request that
// happened to touch byte offset 0 -- nothing guaranteed that ever happened.
// A session whose first ReadAt lands at a nonzero offset (a resumed stream,
// a player seeking its metadata table before reading from the start, a
// chunked VFS read fetching a later chunk first) must still verify the
// wrong key and fail, not silently serve unverified plaintext for the rest
// of the session.
func TestEncryptedRarReaderRejectsWrongKeyWhenFirstReadIsNotAtOffsetZero(t *testing.T) {
	key, iv := testKeyIV()
	_, inner := buildEncryptedFixture(t, key, iv, rarcrypto.BlockSize*8)

	wrongKey := key
	wrongKey[0] ^= 0xFF
	r := NewEncryptedRarReader(inner, wrongKey, iv)

	// First read of the session deliberately does NOT touch offset 0.
	offset := int64(rarcrypto.BlockSize*3 + 5)
	dst := make([]byte, 20)
	_, err := r.ReadAt(context.Background(), dst, offset)
	if err == nil {
		t.Fatal("expected verification to catch the wrong key even though the first read was mid-file, not at offset 0")
	}
}

// TestEncryptedRarReaderVerifiesExactlyOnceAcrossOffsets guards that
// verification -- now decoupled from whichever offset a read happens to
// request -- still only ever fetches/decrypts byte 0 once per reader, not
// once per distinct offset read.
func TestEncryptedRarReaderVerifiesExactlyOnceAcrossOffsets(t *testing.T) {
	key, iv := testKeyIV()
	plaintext, innerFile := buildEncryptedFixture(t, key, iv, rarcrypto.BlockSize*8)
	counting := &countingReadAtFile{VirtualMediaFile: innerFile}
	r := NewEncryptedRarReader(counting, key, iv)

	// Read from several different, non-zero offsets -- none of these
	// requests offset 0 itself, so the only offset-0 read should be
	// ensureVerified's own dedicated, one-time fetch.
	for _, offset := range []int64{int64(rarcrypto.BlockSize * 5), int64(rarcrypto.BlockSize*2 + 3), int64(rarcrypto.BlockSize * 6)} {
		dst := make([]byte, rarcrypto.BlockSize)
		length := int64(len(dst))
		if offset+length > int64(len(plaintext)) {
			length = int64(len(plaintext)) - offset
		}
		if _, err := r.ReadAt(context.Background(), dst[:length], offset); err != nil && err != io.EOF {
			t.Fatalf("unexpected error reading offset %d: %v", offset, err)
		}
	}

	if got := counting.zeroOffsetReads; got != 1 {
		t.Fatalf("expected exactly 1 read at offset 0 (the dedicated verification fetch) across %d total ReadAt calls at varying offsets, got %d", counting.totalReads, got)
	}
}

// countingReadAtFile wraps a VirtualMediaFile and counts how many ReadAt
// calls land at offset 0 specifically, to prove ensureVerified's fetch runs
// exactly once regardless of how many other offsets are read.
type countingReadAtFile struct {
	VirtualMediaFile
	zeroOffsetReads int
	totalReads      int
}

func (c *countingReadAtFile) ReadAt(ctx context.Context, dst []byte, offset int64) (int, error) {
	c.totalReads++
	if offset == 0 {
		c.zeroOffsetReads++
	}
	return c.VirtualMediaFile.ReadAt(ctx, dst, offset)
}

// TestEncryptedRarReaderVerificationIsStickyAndCached guards that a failed
// verification is remembered (never re-attempts a decrypt that already
// proved wrong) and that a SUCCESSFUL verification also only runs once,
// not on every read touching offset 0.
func TestEncryptedRarReaderVerificationIsStickyAndCached(t *testing.T) {
	key, iv := testKeyIV()
	_, inner := buildEncryptedFixture(t, key, iv, rarcrypto.BlockSize*4)
	wrongKey := key
	wrongKey[0] ^= 0xFF
	r := NewEncryptedRarReader(inner, wrongKey, iv)

	dst := make([]byte, rarcrypto.BlockSize)
	_, err1 := r.ReadAt(context.Background(), dst, 0)
	_, err2 := r.ReadAt(context.Background(), dst, 0)
	if err1 == nil || err2 == nil {
		t.Fatal("expected both reads to fail verification")
	}
	if err1 != err2 {
		t.Fatalf("expected the identical cached error on repeat reads, got %v then %v", err1, err2)
	}
}
