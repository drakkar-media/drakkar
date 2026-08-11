// Package rarcrypto implements the AES-256-CBC decryption RAR5 uses for
// password-protected archives -- key derivation from a password (RAR5's own
// PBKDF2-like HMAC-SHA256 construction) and block decryption. Pure Go
// standard-library crypto only (crypto/aes, crypto/cipher, crypto/hmac,
// crypto/sha256) -- no external binary, no third-party crypto dependency.
//
// The key-derivation algorithm here was verified against a real known-good
// (password, salt, expected password-check value) triple from a genuine
// RAR5 encrypted archive (ported from an equivalent, independently-tested
// open-source implementation) -- see TestDeriveKeyMatchesKnownGoodVector.
// What is NOT independently verified against a real archive: that a
// "stored" (uncompressed) encrypted file's data area is one continuous
// AES-256-CBC stream keyed off the file's own IV with no internal
// re-chunking/re-keying. This is the standard, documented behavior and
// consistent with how RAR5's own header encryption works (also one
// continuous CBC block), but callers MUST verify decrypted output against
// the container's own magic bytes before trusting/serving it -- see
// DecryptBlockAligned's doc comment and stream.NewEncryptedRarReader, which
// enforces this.
package rarcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
)

// BlockSize is AES's block size (also RAR5's CBC chaining unit).
const BlockSize = 16

// KeySize is the AES-256 key size RAR5 always uses for its own encryption.
const KeySize = 32

// ErrIncorrectPassword indicates a header's password-check value didn't
// match the derived key -- the password is definitely wrong (not a
// corrupted-data ambiguity).
var ErrIncorrectPassword = errors.New("rarcrypto: incorrect password")

// EncryptionParams is everything RAR5's per-file "encryption" extra-area
// record (type 1) carries, needed to derive the AES key and decrypt that
// file's data.
type EncryptionParams struct {
	// Lg2Count is log2 of the PBKDF2-like iteration count.
	Lg2Count uint8
	// Salt is the 16-byte KDF salt.
	Salt [16]byte
	// IV is the 16-byte AES-CBC initialization vector for this file's data
	// (distinct from any IV used for header encryption).
	IV [16]byte
	// HasPasswordCheck reports whether PasswordCheck is present/meaningful.
	HasPasswordCheck bool
	// PasswordCheck is the 8-byte password-check value RAR5 stores so a
	// wrong password can be detected before attempting to decrypt/serve
	// any actual data.
	PasswordCheck [8]byte
}

// deriveRounds runs RAR5's 3-round HMAC-SHA256 KDF construction (round 0:
// 2^lg2Count iterations, producing the AES key; rounds 1-2: 16 further
// iterations each, round 2's result feeding the password-check value) and
// returns all 3 rounds' final XOR-folded hash blocks.
func deriveRounds(password string, salt [16]byte, lg2Count uint8) [3][32]byte {
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
	return results
}

// DeriveKey derives the AES-256 key for password against params, verifying
// the password-check value first when params.HasPasswordCheck is set.
// Returns ErrIncorrectPassword if the check value doesn't match -- a
// definite signal the password is wrong, not just "might be corrupted
// data", so callers should treat it as a hard reject rather than retrying.
func DeriveKey(password string, params EncryptionParams) ([KeySize]byte, error) {
	results := deriveRounds(password, params.Salt, params.Lg2Count)
	if params.HasPasswordCheck {
		var check [8]byte
		for i, b := range results[2] {
			check[i%8] ^= b
		}
		if check != params.PasswordCheck {
			return [KeySize]byte{}, ErrIncorrectPassword
		}
	}
	return results[0], nil
}

// DecryptBlockAligned decrypts ciphertext (which MUST be a non-empty
// multiple of BlockSize) using AES-256-CBC with key and chainIV as the
// block-0 chaining input, returning the plaintext in a new slice the same
// length as ciphertext.
//
// chainIV is the file's own stored IV only when decrypting from the very
// start of the file's data area (offset 0); for any other block-aligned
// offset, chainIV must be the ciphertext of the immediately preceding
// BlockSize-byte block -- standard CBC decryption only needs that one
// preceding ciphertext block, not a decrypt chain from the start, so
// random-access reads are cheap (see stream.NewEncryptedRarReader).
//
// This function has NO way to detect a wrong key/IV/chaining assumption on
// its own -- AES-CBC decryption "succeeds" and produces SOME output
// regardless of whether the key is right. Callers MUST independently verify
// the result (e.g. against the container's own magic bytes) before trusting
// it; see this package's doc comment for exactly what is and isn't verified
// about RAR5's specific chaining behavior.
func DecryptBlockAligned(ciphertext []byte, key [KeySize]byte, chainIV [BlockSize]byte) ([]byte, error) {
	if len(ciphertext) == 0 || len(ciphertext)%BlockSize != 0 {
		return nil, errors.New("rarcrypto: ciphertext must be a non-empty multiple of the block size")
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	mode := cipher.NewCBCDecrypter(block, chainIV[:])
	out := make([]byte, len(ciphertext))
	mode.CryptBlocks(out, ciphertext)
	return out, nil
}

// ErrUnrecognizedContainer indicates decrypted bytes at a file's start
// don't match any known video container's magic number -- the mandatory
// signal that the key, IV, or this package's chaining assumption about
// RAR5's per-file encryption (see the package doc comment) was wrong. AES
// decryption itself never fails on a wrong key -- it silently produces
// incorrect plaintext -- so this check is the ONLY thing standing between a
// wrong key and confidently serving corrupted garbage to a player.
var ErrUnrecognizedContainer = errors.New("rarcrypto: decrypted output doesn't match a known video container -- wrong password, or a RAR5 encryption-layout assumption doesn't hold for this file")

// VerifyContainerHeader checks whether header (the decrypted bytes starting
// at a file's very first byte) begins with a recognized video container's
// magic number. Callers must treat a mismatch as a hard failure, never as
// "probably fine" -- see ErrUnrecognizedContainer.
func VerifyContainerHeader(header []byte) error {
	if len(header) < 4 {
		return fmt.Errorf("%w (too short to identify)", ErrUnrecognizedContainer)
	}
	// MKV / WebM -- EBML magic.
	if header[0] == 0x1a && header[1] == 0x45 && header[2] == 0xdf && header[3] == 0xa3 {
		return nil
	}
	// AVI -- RIFF header.
	if string(header[0:4]) == "RIFF" {
		return nil
	}
	// MP4 / MOV -- ISO Base Media File Format: box type at bytes 4-7.
	if len(header) >= 8 {
		switch string(header[4:8]) {
		case "ftyp", "moov", "mdat", "free", "wide", "skip", "pnot":
			return nil
		}
	}
	return fmt.Errorf("%w (got: %02x %02x %02x %02x)", ErrUnrecognizedContainer, header[0], header[1], header[2], header[3])
}
