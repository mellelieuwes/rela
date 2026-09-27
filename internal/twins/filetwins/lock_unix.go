//go:build !windows

package filetwins

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive BSD lock, waiting for a holder to release it.
//
// flock rather than fcntl record locks, for the reason sqlitedb gives: a POSIX
// lock is dropped when ANY descriptor to the file closes in the process, while
// flock belongs to the open file description — which is also what makes two
// Stores in ONE process exclude each other, since each opens its own.
func lockFile(f *os.File) error {
	//nolint:gosec // G115: a file descriptor is a small non-negative int by construction
	return unix.Flock(int(f.Fd()), unix.LOCK_EX)
}

func unlockFile(f *os.File) error {
	//nolint:gosec // G115: a file descriptor is a small non-negative int by construction
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
