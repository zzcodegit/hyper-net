package obfuscate

import (
	"io"
	"math/rand"
	"sync"
	"time"
)

// DelayConn оборачивает io.ReadWriteCloser и перед каждым Write выжидает
// случайную задержку в [DelayMin, DelayMax] для усложнения трафик-анализа.
type DelayConn struct {
	io.ReadWriteCloser
	DelayMin, DelayMax time.Duration
	rng                *rand.Rand
	mu                 sync.Mutex
}

// NewDelayConn создаёт обёртку с задержкой перед записью. Если max <= 0, задержка отключена (pass-through).
func NewDelayConn(rwc io.ReadWriteCloser, delayMin, delayMax time.Duration) *DelayConn {
	if delayMax <= 0 {
		delayMin = 0
		delayMax = 0
	}
	if delayMin > delayMax {
		delayMin = delayMax
	}
	return &DelayConn{
		ReadWriteCloser: rwc,
		DelayMin:        delayMin,
		DelayMax:        delayMax,
		rng:             rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Write перед отправкой данных ждёт случайное время в [DelayMin, DelayMax].
func (c *DelayConn) Write(p []byte) (n int, err error) {
	if c.DelayMax > 0 {
		c.mu.Lock()
		d := c.DelayMin
		if c.DelayMax > c.DelayMin {
			d += time.Duration(c.rng.Int63n(int64(c.DelayMax - c.DelayMin + 1)))
		}
		c.mu.Unlock()
		if d > 0 {
			time.Sleep(d)
		}
	}
	return c.ReadWriteCloser.Write(p)
}

