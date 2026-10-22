package hls

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"goaria-v3/internal/surge/types"
)

// segmentDataError classifies a retryable ingest data-plane failure: bad key
// material or index, malformed IV/sequence metadata, truncated or misaligned
// ciphertext, and PKCS#7 padding violations. It deliberately never wraps a
// permanent HLS sentinel — the transport layer may re-fetch the segment and
// retry. Messages never carry key bytes, plaintext, or credential URLs.
type segmentDataError struct {
	segIndex int
	reason   string
}

func (e *segmentDataError) Error() string {
	return fmt.Sprintf("hls segment %d data error: %s", e.segIndex, e.reason)
}

func newSegmentDataError(segIndex int, format string, args ...any) *segmentDataError {
	return &segmentDataError{segIndex: segIndex, reason: fmt.Sprintf(format, args...)}
}

// decryptSegmentInPlace decrypts an AES-128-CBC media segment payload in
// place and returns the plaintext view sharing buf's backing array together
// with the stripped padding length. buf is borrowed for the call only.
//
// seg.KeyIndex is 1-based: 0 passes buf through untouched (any length,
// including empty). For encrypted segments keyBytes must align with the
// playlist Keys order so Keys[KeyIndex-1] resolves to a 16-byte AES-128 key.
// An explicit IV must hex-decode to exactly 16 bytes — the state contract is
// normalized lowercase hex, but this gate accepts any decodable form; an
// empty IV derives the RFC 8216 implicit IV from seg.Sequence
// as a 128-bit big-endian value. PKCS#7 padding is verified only after
// CryptBlocks — a padding failure leaves buf scrambled and callers must
// discard it and re-fetch rather than decrypt the same buffer twice. A fresh
// BlockMode is created per call; nothing is shared across goroutines.
func decryptSegmentInPlace(buf []byte, seg *types.HLSSegmentState, keyBytes [][]byte) ([]byte, int, error) {
	if seg == nil {
		return nil, 0, newSegmentDataError(-1, "missing segment state")
	}
	if seg.KeyIndex == 0 {
		return buf, 0, nil
	}
	idx := seg.Index
	if seg.KeyIndex < 0 || seg.KeyIndex > len(keyBytes) {
		return nil, 0, newSegmentDataError(idx, "key index %d out of range", seg.KeyIndex)
	}
	key := keyBytes[seg.KeyIndex-1]
	if len(key) != aes.BlockSize {
		return nil, 0, newSegmentDataError(idx, "key material is %d bytes, want %d", len(key), aes.BlockSize)
	}
	if len(buf) == 0 {
		return nil, 0, newSegmentDataError(idx, "empty ciphertext")
	}
	if len(buf)%aes.BlockSize != 0 {
		return nil, 0, newSegmentDataError(idx, "ciphertext %d bytes not %d-byte aligned", len(buf), aes.BlockSize)
	}

	var iv [aes.BlockSize]byte
	if seg.IVHex != "" {
		decoded, err := hex.DecodeString(seg.IVHex)
		if err != nil || len(decoded) != aes.BlockSize {
			return nil, 0, newSegmentDataError(idx, "explicit IV not 16-byte hex")
		}
		copy(iv[:], decoded)
	} else {
		if seg.Sequence < 0 {
			return nil, 0, newSegmentDataError(idx, "negative media sequence %d", seg.Sequence)
		}
		binary.BigEndian.PutUint64(iv[8:], uint64(seg.Sequence))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, 0, newSegmentDataError(idx, "aes init: %v", err)
	}
	cipher.NewCBCDecrypter(block, iv[:]).CryptBlocks(buf, buf)

	padLen := int(buf[len(buf)-1])
	if padLen < 1 || padLen > aes.BlockSize || padLen > len(buf) {
		return nil, 0, newSegmentDataError(idx, "pkcs7 padding length %d", padLen)
	}
	for _, b := range buf[len(buf)-padLen:] {
		if int(b) != padLen {
			return nil, 0, newSegmentDataError(idx, "pkcs7 padding bytes inconsistent")
		}
	}
	return buf[:len(buf)-padLen], padLen, nil
}
