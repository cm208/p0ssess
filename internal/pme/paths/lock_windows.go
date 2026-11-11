//go:build windows

package paths

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

const lockfileExclusiveLock = 0x2

// flock takes an exclusive lock on f with LockFileEx, which other p0ssess processes honor.
func flock(f *os.File) (func(), error) {
	h := f.Fd()
	ol := new(syscall.Overlapped)
	r, _, err := procLockFileEx.Call(h, lockfileExclusiveLock, 0, 1, 0, uintptr(unsafe.Pointer(ol)))
	if r == 0 {
		f.Close()
		return nil, err
	}
	return func() {
		procUnlockFileEx.Call(h, 0, 1, 0, uintptr(unsafe.Pointer(ol)))
		f.Close()
	}, nil
}

// openDirLock opens a lock file that stands for dir. Windows cannot lock a directory, so the file
// lives in the user's temp directory, named after the directory's path, and nothing is left beside
// the files being locked.
func openDirLock(dir string) (*os.File, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(strings.ToLower(abs)))
	ld := filepath.Join(os.TempDir(), "p0ssess-locks")
	if err := os.MkdirAll(ld, 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(ld, hex.EncodeToString(sum[:12])+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
}
