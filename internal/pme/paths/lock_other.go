//go:build !unix

package paths

import (
	"errors"
	"os"
)

// flock fails on platforms without it, so a shared file is never rewritten without the lock.
func flock(f *os.File) (func(), error) {
	f.Close()
	return nil, errors.New("file locking needs a Unix system")
}
