package attachment_test

import (
	"testing"

	"github.com/Sourcehaven-BV/rela/internal/attachment"
)

func TestLimiter_AdmitsUpToCapacity(t *testing.T) {
	l := attachment.NewLimiter(2)
	r1, ok1 := l.TryAcquire()
	r2, ok2 := l.TryAcquire()
	if !ok1 || !ok2 {
		t.Fatal("the first two acquires must succeed")
	}
	if _, ok := l.TryAcquire(); ok {
		t.Fatal("a third acquire succeeded at capacity 2")
	}
	r1()
	r1() // idempotent: must not free a second slot
	r3, ok := l.TryAcquire()
	if !ok {
		t.Fatal("acquire after a release failed")
	}
	if _, ok := l.TryAcquire(); ok {
		t.Fatal("a double release freed an extra slot")
	}
	r2()
	r3()
}
