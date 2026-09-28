package attachment

import "sync"

// Limiter bounds how many uploads one process handles at once. An upload
// holds several full copies of its bytes (the request body, the processor's
// buffer, the spool file), so without a bound, concurrent uploads could use
// up memory and temporary disk. Callers take a slot before reading the body
// and refuse the upload when none is free, rather than queueing it.
//
// Every upload path in a process must share one Limiter, or each path gets
// its own budget.
type Limiter struct {
	slots chan struct{}
}

// DefaultMaxUploads is the per-process upload bound the servers use. At the
// default 64 MiB cap it keeps upload buffers within a few hundred MiB.
const DefaultMaxUploads = 4

// NewLimiter returns a Limiter admitting n uploads at once; n below 1
// counts as 1.
func NewLimiter(n int) *Limiter {
	return &Limiter{slots: make(chan struct{}, max(1, n))}
}

// TryAcquire takes a slot without waiting. It returns the slot's idempotent
// release and true, or nil and false when every slot is taken.
func (l *Limiter) TryAcquire() (release func(), ok bool) {
	select {
	case l.slots <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-l.slots }) }, true
	default:
		return nil, false
	}
}
