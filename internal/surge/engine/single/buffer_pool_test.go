package single

import (
	"testing"

	"goaria-v3/internal/surge/engine/types"
	"goaria-v3/internal/surge/utils"
)

func TestPutBufferSafe_NormalBuffer(t *testing.T) {
	normal := make([]byte, 256*utils.KiB)
	normalPtr := &normal
	putBufferSafe(&bufPool, normalPtr)

	got := bufPool.Get().(*[]byte)
	if cap(*got) != 256*utils.KiB {
		t.Errorf("expected cap=%d after putBufferSafe, got cap=%d", 256*utils.KiB, cap(*got))
	}
}

func TestPutBufferSafe_OversizedBuffer(t *testing.T) {
	big := make([]byte, types.MaxPoolBufferCap+1)
	bigPtr := &big
	putBufferSafe(&bufPool, bigPtr)

	got := bufPool.Get().(*[]byte)
	if cap(*got) != 256*utils.KiB {
		t.Errorf("expected cap=%d (fresh) after discarding oversized, got cap=%d", 256*utils.KiB, cap(*got))
	}
}

func TestBufPool_DefaultSize(t *testing.T) {
	got := bufPool.Get().(*[]byte)
	if len(*got) != 256*utils.KiB {
		t.Errorf("expected default len=%d (256KB), got len=%d", 256*utils.KiB, len(*got))
	}
	if cap(*got) != 256*utils.KiB {
		t.Errorf("expected default cap=%d (256KB), got cap=%d", 256*utils.KiB, cap(*got))
	}
}
