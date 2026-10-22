package hls

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"unsafe"

	"goaria-v3/internal/surge/types"
)

// The ciphertext vectors below were produced externally with
// `openssl enc -aes-128-cbc -K <key> -iv <iv>` (default PKCS#7 padding) so the
// success fixtures do not come out of the code path under test.
const (
	decTestKey1 = "00112233445566778899aabbccddeeff"
	decTestKey2 = "ffeeddccbbaa99887766554433221100"
	decTestIV   = "0102030405060708090a0b0c0d0e0f10"
)

func decHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex.DecodeString(%q) = %v", s, err)
	}
	return b
}

func decKeyTable(t *testing.T) [][]byte {
	t.Helper()
	return [][]byte{decHex(t, decTestKey1), decHex(t, decTestKey2)}
}

// encryptRaw builds malformed-padding fixtures with the standard library:
// the ciphertext decrypts to pt exactly (no padding is appended).
func encryptRaw(t *testing.T, key, iv, pt []byte) []byte {
	t.Helper()
	if len(pt)%aes.BlockSize != 0 {
		t.Fatalf("fixture plaintext %d not block aligned", len(pt))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	ct := make([]byte, len(pt))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, pt)
	return ct
}

func decSeg(index, keyIndex int, ivHex string, seq int64) *types.HLSSegmentState {
	return &types.HLSSegmentState{Index: index, KeyIndex: keyIndex, IVHex: ivHex, Sequence: seq}
}

func assertDecDataErr(t *testing.T, plain []byte, padLen int, err error, ctx string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: err = nil, want segment data error", ctx)
	}
	if _, ok := errors.AsType[*segmentDataError](err); !ok {
		t.Fatalf("%s: err type %T not *segmentDataError", ctx, err)
	}
	if types.IsPermanentHLSError(err) {
		t.Fatalf("%s: err must stay retryable, got permanent HLS: %v", ctx, err)
	}
	if plain != nil || padLen != 0 {
		t.Fatalf("%s: on error want nil plaintext/padLen 0, got len=%d pad=%d", ctx, len(plain), padLen)
	}
}

func TestDecryptSegmentInPlace_ExplicitIVVectors(t *testing.T) {
	ivHex := decTestIV
	cases := []struct {
		name   string
		ct     string
		want   []byte
		padLen int
	}{
		{
			name:   "18byte-pad14",
			ct:     "7037e8134a2a09f252b9ef40bde3200f5cca07897f003b5f140f2807430b50a3",
			want:   []byte("hello-hls-segment!"),
			padLen: 14,
		},
		{
			name:   "1byte-pad15",
			ct:     "6302251fde1002c96694d4e582bd69be",
			want:   []byte("A"),
			padLen: 15,
		},
		{
			name:   "empty-pad16",
			ct:     "b9ab5f90fd9ba09313c3f870427c1a78",
			want:   []byte{},
			padLen: 16,
		},
		{
			name:   "16byte-pad16",
			ct:     "1e527b1499830859517e100905fda90f2e388989269325181edc799208c0e26b",
			want:   []byte("0123456789abcdef"),
			padLen: 16,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := decHex(t, tc.ct)
			plain, padLen, err := decryptSegmentInPlace(buf, decSeg(0, 1, ivHex, 0), decKeyTable(t))
			if err != nil {
				t.Fatalf("decryptSegmentInPlace() = %v", err)
			}
			if !bytes.Equal(plain, tc.want) {
				t.Fatalf("plaintext = %x, want %x", plain, tc.want)
			}
			if padLen != tc.padLen {
				t.Fatalf("padLen = %d, want %d", padLen, tc.padLen)
			}
			if len(plain) > 0 && unsafe.SliceData(plain) != unsafe.SliceData(buf) {
				t.Fatal("plaintext view does not share the input backing array")
			}
		})
	}
}

func TestDecryptSegmentInPlace_ImplicitIVBigEndian(t *testing.T) {
	cases := []struct {
		name string
		seq  int64
		ct   string
	}{
		// openssl: -K key2 -iv 00000000000000000000000000000103
		{"seq259", 259, "acd7a5f6c486a7292de8f99623c722e418a7bc497bd91f8fd3faf7258f19889c"},
		// openssl: -K key2 -iv 00000000000000000000000100000003 (seq 2^32+3)
		{"seq4G+3", 1<<32 + 3, "e13a446dbc965fced55213732c36b1342652ab1b8465a24a0cf61ebe7ba2123b"},
	}
	want := []byte("implicit-iv-seg!!")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := decHex(t, tc.ct)
			plain, padLen, err := decryptSegmentInPlace(buf, decSeg(3, 2, "", tc.seq), decKeyTable(t))
			if err != nil {
				t.Fatalf("decryptSegmentInPlace() = %v", err)
			}
			if !bytes.Equal(plain, want) || padLen != 15 {
				t.Fatalf("plaintext = %x padLen = %d, want %x/15", plain, padLen, want)
			}
		})
	}
}

func TestDecryptSegmentInPlace_KeyIndexSelection(t *testing.T) {
	// Segment bound to Keys[1]; decrypting with the wrong slot must fail
	// padding (or would produce garbage), with the right slot succeeds.
	buf := decHex(t, "acd7a5f6c486a7292de8f99623c722e418a7bc497bd91f8fd3faf7258f19889c")
	plain, _, err := decryptSegmentInPlace(buf, decSeg(0, 2, "", 259), decKeyTable(t))
	if err != nil {
		t.Fatalf("decrypt with Keys[1] = %v", err)
	}
	if string(plain) != "implicit-iv-seg!!" {
		t.Fatalf("plaintext = %q", plain)
	}

	// KeyIndex 1 selects a different key for the same ciphertext: the
	// decrypt is structurally valid but almost surely fails padding; the
	// contract only promises a data error or wrong bytes, never a panic.
	buf2 := decHex(t, "acd7a5f6c486a7292de8f99623c722e418a7bc497bd91f8fd3faf7258f19889c")
	if _, _, err := decryptSegmentInPlace(buf2, decSeg(0, 1, "", 259), decKeyTable(t)); err != nil {
		assertDecDataErr(t, nil, 0, err, "wrong-key padding")
	}
}

func TestDecryptSegmentInPlace_PlaintextPassthrough(t *testing.T) {
	// KeyIndex 0 returns the input untouched for any length, never
	// touching key material or IV state.
	for _, buf := range [][]byte{nil, {}, {0x00}, {0x01, 0x02, 0x03}} {
		plain, padLen, err := decryptSegmentInPlace(buf, decSeg(0, 0, "deadbeef", -5), nil)
		if err != nil {
			t.Fatalf("passthrough err = %v", err)
		}
		if padLen != 0 || !bytes.Equal(plain, buf) {
			t.Fatalf("passthrough = (%x,%d), want (%x,0)", plain, padLen, buf)
		}
		if len(buf) > 0 && unsafe.SliceData(plain) != unsafe.SliceData(buf) {
			t.Fatal("passthrough must return the identical array")
		}
	}
}

func TestDecryptSegmentInPlace_Errors(t *testing.T) {
	key := decHex(t, decTestKey1)
	iv := decHex(t, decTestIV)
	keys := decKeyTable(t)
	validCT := decHex(t, "7037e8134a2a09f252b9ef40bde3200f5cca07897f003b5f140f2807430b50a3")

	t.Run("BadKeyIndexAndMaterial", func(t *testing.T) {
		cases := []struct {
			name string
			seg  *types.HLSSegmentState
			keys [][]byte
		}{
			{"NilSeg", nil, keys},
			{"NegativeIndex", decSeg(0, -1, "", 0), keys},
			{"IndexBeyondTable", decSeg(0, 3, "", 0), keys},
			{"NilKey", decSeg(0, 1, "", 0), [][]byte{nil}},
			{"Key15", decSeg(0, 1, "", 0), [][]byte{make([]byte, 15)}},
			{"Key17", decSeg(0, 1, "", 0), [][]byte{make([]byte, 17)}},
			{"Key32", decSeg(0, 1, "", 0), [][]byte{make([]byte, 32)}},
		}
		for _, tc := range cases {
			plain, padLen, err := decryptSegmentInPlace(append([]byte(nil), validCT...), tc.seg, tc.keys)
			assertDecDataErr(t, plain, padLen, err, tc.name)
		}
	})

	t.Run("CiphertextAlignment", func(t *testing.T) {
		for _, n := range []int{0, 1, 15, 17, 31} {
			buf := make([]byte, n)
			plain, padLen, err := decryptSegmentInPlace(buf, decSeg(0, 1, "", 0), keys)
			assertDecDataErr(t, plain, padLen, err, fmt.Sprintf("ciphertext len %d", n))
		}
	})

	t.Run("BadIVAndSequence", func(t *testing.T) {
		cases := []struct {
			name string
			seg  *types.HLSSegmentState
		}{
			{"IVNotHex", decSeg(0, 1, "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", 0)},
			{"IVShort", decSeg(0, 1, "aabb", 0)},
			{"IV31Chars", decSeg(0, 1, "0123456789abcdef0123456789abcde", 0)},
			{"IV33Chars", decSeg(0, 1, "0123456789abcdef0123456789abcdef0", 0)},
			{"NegativeSequence", decSeg(0, 1, "", -1)},
		}
		for _, tc := range cases {
			plain, padLen, err := decryptSegmentInPlace(append([]byte(nil), validCT...), tc.seg, keys)
			assertDecDataErr(t, plain, padLen, err, tc.name)
		}
	})

	t.Run("PaddingBoundaries", func(t *testing.T) {
		mkCT := func(tail []byte) []byte {
			pt := make([]byte, aes.BlockSize)
			copy(pt[aes.BlockSize-len(tail):], tail)
			return encryptRaw(t, key, iv, pt)
		}
		cases := []struct {
			name string
			ct   []byte
		}{
			// Last decrypted byte 0x00 -> padLen 0.
			{"PadZero", mkCT([]byte{0x00})},
			// 0x11 exceeds the 16-byte padding bound.
			{"Pad17", mkCT([]byte{0x11})},
			// padLen 2 but the 2-byte tail is not uniform.
			{"PadInconsistent", mkCT([]byte{0x05, 0x02})},
			// padLen 16 covering the whole block, mixed tail bytes.
			{"Pad16Mixed", mkCT(func() []byte {
				b := bytes.Repeat([]byte{0x10}, aes.BlockSize)
				b[0] = 0x0f
				return b
			}())},
		}
		for _, tc := range cases {
			plain, padLen, err := decryptSegmentInPlace(tc.ct, decSeg(0, 1, decTestIV, 0), keys)
			assertDecDataErr(t, plain, padLen, err, tc.name)
		}
	})

	t.Run("BadPaddingLeavesNoClaim", func(t *testing.T) {
		// Corrupt a valid ciphertext's tail byte: decryption is in place,
		// so a padding failure must report nil plaintext (the buffer is
		// scrambled — callers re-fetch, never re-decrypt it).
		buf := append([]byte(nil), validCT...)
		buf[len(buf)-1] ^= 0xFF
		plain, padLen, err := decryptSegmentInPlace(buf, decSeg(0, 1, decTestIV, 0), keys)
		assertDecDataErr(t, plain, padLen, err, "corrupted tail")
	})
}

func TestDecryptSegmentInPlace_ConcurrentDistinctBuffers(t *testing.T) {
	// Distinct buffers, shared read-only key table: no shared BlockMode
	// state may leak across goroutines.
	const n = 16
	ctHex := "7037e8134a2a09f252b9ef40bde3200f5cca07897f003b5f140f2807430b50a3"
	want := []byte("hello-hls-segment!")
	keys := decKeyTable(t)
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			buf := decHexT(ctHex)
			plain, padLen, err := decryptSegmentInPlace(buf, decSeg(i, 1, decTestIV, 0), keys)
			switch {
			case err != nil:
				errs <- err
			case !bytes.Equal(plain, want) || padLen != 14:
				errs <- errors.New("plaintext mismatch")
			case unsafe.SliceData(plain) != unsafe.SliceData(buf):
				errs <- errors.New("view does not share backing array")
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// decHexT is a goroutine-safe variant without *testing.T in scope.
func decHexT(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}
