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
