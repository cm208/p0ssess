//go:build unix

package paths

import (
	"os"
	"syscall"
)

// flock takes an exclusive advisory lock on f that other p0ssess processes honor.
func flock(f *os.File) (func(), error) {
	var err error
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// openDirLock opens the directory itself, which flock can lock.
func openDirLock(dir string) (*os.File, error) { return os.Open(dir) }
