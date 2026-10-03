// Wake-up timer clock abstractions for monotonic and fakeable time.
package wake

import (
	"sync"
	"time"
)

type Clock interface{ Now() time.Time }

type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

type FakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func NewFakeClock(t time.Time) *FakeClock { return &FakeClock{t: t} }
func (f *FakeClock) Now() time.Time       { f.mu.Lock(); defer f.mu.Unlock(); return f.t }
func (f *FakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.t = f.t.Add(d)
	f.mu.Unlock()
}
