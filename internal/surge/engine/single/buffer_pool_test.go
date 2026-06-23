package single

import (
	"testing"

	"goaria-v3/internal/surge/engine/types"
)

func TestPutBufferSafe_NormalBuffer(t *testing.T) {
	normal := make([]byte, 256*types.KB)
	normalPtr := &normal
	putBufferSafe(&bufPool, normalPtr)

	got := bufPool.Get().(*[]byte)
	if cap(*got) != 256*types.KB {
		t.Errorf("expected cap=%d after putBufferSafe, got cap=%d", 256*types.KB, cap(*got))
	}
}

func TestPutBufferSafe_OversizedBuffer(t *testing.T) {
	big := make([]byte, types.MaxPoolBufferCap+1)
	bigPtr := &big
	putBufferSafe(&bufPool, bigPtr)

	got := bufPool.Get().(*[]byte)
	if cap(*got) != 256*types.KB {
		t.Errorf("expected cap=%d (fresh) after discarding oversized, got cap=%d", 256*types.KB, cap(*got))
	}
}

func TestBufPool_DefaultSize(t *testing.T) {
	got := bufPool.Get().(*[]byte)
	if len(*got) != 256*types.KB {
		t.Errorf("expected default len=%d (256KB), got len=%d", 256*types.KB, len(*got))
	}
	if cap(*got) != 256*types.KB {
		t.Errorf("expected default cap=%d (256KB), got cap=%d", 256*types.KB, cap(*got))
	}
}
