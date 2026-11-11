package paths

import (
	"os"
	"path/filepath"
	"sync"
)

var (
	locksMu sync.Mutex
	locks   = map[string]*sync.Mutex{}
)

func procLock(key string) *sync.Mutex {
	locksMu.Lock()
	defer locksMu.Unlock()
	m := locks[key]
	if m == nil {
		m = &sync.Mutex{}
		locks[key] = m
	}
	return m
}

// LockFile takes an exclusive lock on the lock file at path, creating it, held in this process and,
// through flock, across processes. The ledger uses it on history.jsonl.lock.
func LockFile(path string) (func(), error) {
	key, _ := filepath.Abs(path)
	m := procLock(key)
	m.Lock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		m.Unlock()
		return nil, err
	}
	unlock, err := flock(f)
	if err != nil {
		m.Unlock()
		return nil, err
	}
	return func() { unlock(); m.Unlock() }, nil
}

// WithLock runs fn, which reads and rewrites path, while holding the same kind of lock on path's
// directory. Locking the directory leaves no lock file beside a project's rules. Writes inside fn go
// through WriteFileAtomic, so a reader that does not lock never sees a torn file.
func WithLock(path string, fn func() error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	key, _ := filepath.Abs(dir)
	m := procLock(key)
	m.Lock()
	defer m.Unlock()
	f, err := openDirLock(dir)
	if err != nil {
		return err
	}
	unlock, err := flock(f)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}
