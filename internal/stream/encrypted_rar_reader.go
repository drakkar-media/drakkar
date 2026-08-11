package stream

import (
	"context"
	"io"
	"sync"

	"github.com/drakkar-media/drakkar/internal/rarcrypto"
)

// EncryptedRarReader decorates another VirtualMediaFile (in practice always
// a *StoredRarReader) whose bytes are AES-256-CBC ciphertext -- a
// password-protected RAR5 archive entry -- transparently decrypting on
// read so callers see the same plaintext they'd get from an unencrypted
// file. inner's own span/segment-fetch/self-correction logic is entirely
// unchanged and reused as-is; this only adds a decrypt pass over whatever
// bytes inner returns.
//
// Random-access reads are cheap: AES-CBC decryption of an arbitrary
// block-aligned offset only needs the ciphertext of the immediately
// preceding block as its chaining input (not a decrypt-from-the-start
// pass) -- see internal/rarcrypto's package doc for why, and for exactly
// what is and isn't independently verified about RAR5's per-file
// encryption layout.
//
// The first read that reaches byte 0 of the file verifies the decrypted
// output against the container's own magic number (rarcrypto.
// VerifyContainerHeader) before returning ANY bytes -- see verifyOnce's
// doc comment for why this is mandatory, not optional.
type EncryptedRarReader struct {
	inner VirtualMediaFile
	key   [rarcrypto.KeySize]byte
	iv    [rarcrypto.BlockSize]byte

	verifyOnce sync.Once
	verifyErr  error
}

// NewEncryptedRarReader wraps inner (whose ReadAt returns ciphertext) with
// AES-256-CBC decryption using key and the file's own stored iv (from its
// RAR5 encryption extra-area record).
func NewEncryptedRarReader(inner VirtualMediaFile, key [rarcrypto.KeySize]byte, iv [rarcrypto.BlockSize]byte) *EncryptedRarReader {
	return &EncryptedRarReader{inner: inner, key: key, iv: iv}
}

func (r *EncryptedRarReader) Name() string { return r.inner.Name() }
func (r *EncryptedRarReader) Size() int64  { return r.inner.Size() }

// ReadAt fills dst with up to len(dst) plaintext bytes starting at offset,
// fetching whatever ciphertext range is needed (block-aligned, plus one
// preceding block for CBC chaining when offset > 0) from inner and
// decrypting it. Semantics otherwise match VirtualMediaFile.ReadAt: a short
// read paired with io.EOF means offset+len(dst) reached or exceeded the
// file's size.
func (r *EncryptedRarReader) ReadAt(ctx context.Context, dst []byte, offset int64) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	const blockSize = rarcrypto.BlockSize
	alignedOffset := offset - offset%blockSize
	lead := offset - alignedOffset

	needBytes := lead + int64(len(dst))
	blocks := (needBytes + blockSize - 1) / blockSize
	cipherLen := blocks * blockSize

	var chainIV [blockSize]byte
	if alignedOffset == 0 {
		chainIV = r.iv
	} else {
		ivBuf := make([]byte, blockSize)
		n, err := r.inner.ReadAt(ctx, ivBuf, alignedOffset-blockSize)
		if err != nil && err != io.EOF {
			return 0, err
		}
		if n < blockSize {
			return 0, io.ErrUnexpectedEOF
		}
		copy(chainIV[:], ivBuf)
	}

	cipherBuf := make([]byte, cipherLen)
	n, err := r.inner.ReadAt(ctx, cipherBuf, alignedOffset)
	if err != nil && err != io.EOF {
		return 0, err
	}
	// A stored RAR entry's ciphertext data area may be padded to a block
	// boundary beyond the file's real plaintext size -- only decrypt
	// whatever full blocks were actually returned.
	usableLen := n - n%blockSize
	if usableLen == 0 {
		return 0, io.EOF
	}
	plaintext, decErr := rarcrypto.DecryptBlockAligned(cipherBuf[:usableLen], r.key, chainIV)
	if decErr != nil {
		return 0, decErr
	}

	if alignedOffset == 0 {
		if verifyErr := r.verify(plaintext); verifyErr != nil {
			return 0, verifyErr
		}
	}

	avail := int64(len(plaintext)) - lead
	if avail <= 0 {
		return 0, io.EOF
	}
	toCopy := int64(len(dst))
	eof := false
	if toCopy >= avail {
		toCopy = avail
		eof = err == io.EOF
	}
	copy(dst, plaintext[lead:lead+toCopy])
	if eof {
		return int(toCopy), io.EOF
	}
	return int(toCopy), nil
}

// verify runs rarcrypto.VerifyContainerHeader against plaintext (which must
// start at the file's own byte 0) exactly once per reader, caching the
// result -- every ReadAt that reaches offset 0 shares this same check
// rather than re-verifying on every call. AES-CBC decryption with a wrong
// key/IV "succeeds" and silently produces incorrect plaintext -- there is
// no other signal that anything is wrong -- so this is the only thing
// standing between a wrong key and confidently serving corrupted bytes to
// a real player. A sticky failure is returned on every subsequent read
// too, not just the first.
func (r *EncryptedRarReader) verify(plaintextFromZero []byte) error {
	r.verifyOnce.Do(func() {
		r.verifyErr = rarcrypto.VerifyContainerHeader(plaintextFromZero)
	})
	return r.verifyErr
}
