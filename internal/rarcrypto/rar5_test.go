package rarcrypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"testing"
)

func mustDecodeHex16(t *testing.T, s string) [16]byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 16 {
		t.Fatalf("expected 16 bytes, got %d", len(b))
	}
	var out [16]byte
	copy(out[:], b)
	return out
}

func mustDecodeHex8(t *testing.T, s string) [8]byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 8 {
		t.Fatalf("expected 8 bytes, got %d", len(b))
	}
	var out [8]byte
	copy(out[:], b)
	return out
}

// TestDeriveKeyMatchesKnownGoodVector is the single most important test in
// this package: it verifies the KDF against a REAL (password, salt,
// password-check) triple recovered from debugging an actual RAR5 encrypted
// archive (ported from an independently-tested open-source implementation
// of the same algorithm) -- not a self-consistency check, an external
// ground truth. If this ever fails, the KDF itself is wrong and nothing
// built on top of it can be trusted.
func TestDeriveKeyMatchesKnownGoodVector(t *testing.T) {
	params := EncryptionParams{
		Lg2Count:         15,
		Salt:             mustDecodeHex16(t, "8246bf4a50c80674189774196e0551b3"),
		HasPasswordCheck: true,
		PasswordCheck:    mustDecodeHex8(t, "c0fa7586afb0c24c"),
	}
	if _, err := DeriveKey("U0b7258526OROQY", params); err != nil {
		t.Fatalf("expected the correct password to derive successfully, got: %v", err)
	}
}

func TestDeriveKeyRejectsWrongPassword(t *testing.T) {
	params := EncryptionParams{
		Lg2Count:         15,
		Salt:             mustDecodeHex16(t, "8246bf4a50c80674189774196e0551b3"),
		HasPasswordCheck: true,
		PasswordCheck:    mustDecodeHex8(t, "c0fa7586afb0c24c"),
	}
	if _, err := DeriveKey("wrong_password", params); err != ErrIncorrectPassword {
		t.Fatalf("expected ErrIncorrectPassword, got: %v", err)
	}
}

func TestDeriveKeyDeterministic(t *testing.T) {
	params := EncryptionParams{Lg2Count: 10, Salt: [16]byte{1, 2, 3}}
	k1, err := DeriveKey("test", params)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := DeriveKey("test", params)
	if err != nil {
		t.Fatal(err)
	}
	if k1 != k2 {
		t.Fatal("expected deterministic key derivation")
	}
}

func TestDeriveKeyDifferentPasswordsDifferentKeys(t *testing.T) {
	params := EncryptionParams{Lg2Count: 10, Salt: [16]byte{1, 2, 3}}
	k1, err := DeriveKey("password1", params)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := DeriveKey("password2", params)
	if err != nil {
		t.Fatal(err)
	}
	if k1 == k2 {
		t.Fatal("expected different passwords to derive different keys")
	}
}

// TestDecryptBlockAlignedRoundTrip verifies DecryptBlockAligned is a correct
// AES-256-CBC decryptor via a standard-library encrypt/decrypt round trip --
// this validates the decrypt primitive itself (block chaining, IV handling)
// independently of any RAR5-specific assumption.
func TestDecryptBlockAlignedRoundTrip(t *testing.T) {
	var key [KeySize]byte
	for i := range key {
		key[i] = byte(i)
	}
	var iv [BlockSize]byte
	for i := range iv {
		iv[i] = byte(0xA0 + i)
	}
	plaintext := []byte("this is exactly 48 bytes of test plaintext!!!!!!")
	if len(plaintext)%BlockSize != 0 {
		t.Fatalf("test plaintext must be block-aligned, got %d bytes", len(plaintext))
	}

	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := make([]byte, len(plaintext))
	cipher.NewCBCEncrypter(block, iv[:]).CryptBlocks(ciphertext, plaintext)

	decrypted, err := DecryptBlockAligned(ciphertext, key, iv)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted = %q, want %q", decrypted, plaintext)
	}
}

// TestDecryptBlockAlignedChainsFromPrecedingCiphertextBlock verifies the
// specific property stream.NewEncryptedRarReader's random-access reads
// depend on: decrypting block N using ciphertext block N-1 as the chain
// input produces the SAME plaintext as decrypting the whole thing from the
// start would for that block -- i.e. CBC decryption of an arbitrary
// mid-stream block never needs anything earlier than its immediate
// predecessor.
func TestDecryptBlockAlignedChainsFromPrecedingCiphertextBlock(t *testing.T) {
	var key [KeySize]byte
	for i := range key {
		key[i] = byte(i * 3)
	}
	var iv [BlockSize]byte
	for i := range iv {
		iv[i] = byte(0x55 + i)
	}
	plaintext := make([]byte, BlockSize*4)
	for i := range plaintext {
		plaintext[i] = byte(i)
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := make([]byte, len(plaintext))
	cipher.NewCBCEncrypter(block, iv[:]).CryptBlocks(ciphertext, plaintext)

	// Decrypt only the last two blocks, using the preceding ciphertext
	// block (block index 1) as the chain IV -- must match the last two
	// blocks of a full decrypt.
	var chainIV [BlockSize]byte
	copy(chainIV[:], ciphertext[BlockSize:BlockSize*2])
	partial, err := DecryptBlockAligned(ciphertext[BlockSize*2:], key, chainIV)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(partial, plaintext[BlockSize*2:]) {
		t.Fatalf("partial decrypt from preceding block = %v, want %v", partial, plaintext[BlockSize*2:])
	}
}

func TestDecryptBlockAlignedRejectsNonAlignedInput(t *testing.T) {
	var key [KeySize]byte
	var iv [BlockSize]byte
	if _, err := DecryptBlockAligned(make([]byte, 15), key, iv); err == nil {
		t.Fatal("expected an error for non-block-aligned ciphertext")
	}
	if _, err := DecryptBlockAligned(nil, key, iv); err == nil {
		t.Fatal("expected an error for empty ciphertext")
	}
}
