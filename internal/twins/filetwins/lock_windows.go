//go:build windows

package filetwins

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive lock on the whole file, waiting for a holder to
// release it. Without LOCKFILE_FAIL_IMMEDIATELY the call blocks; the handle is
// synchronous, so a fresh zero Overlapped is enough.
func lockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, ^uint32(0), ^uint32(0), ol)
}

func unlockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, ^uint32(0), ^uint32(0), ol)
}
