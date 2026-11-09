//go:build !unix

package history

import "errors"

// lockFile fails on platforms without flock, so spend is never checked without the lock.
func lockFile(string) (func(), error) {
	return nil, errors.New("ledger locking needs a Unix system")
}
