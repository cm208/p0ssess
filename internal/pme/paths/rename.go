package paths

import (
	"os"
	"runtime"
	"time"
)

// Rename moves old to new, replacing new. On Windows a replace fails while another process has new
// open (the status line reading the budget, say), so it retries for up to a second there.
func Rename(old, new string) error {
	err := os.Rename(old, new)
	if err == nil || runtime.GOOS != "windows" {
		return err
	}
	for i := 0; i < 20; i++ {
		time.Sleep(50 * time.Millisecond)
		if err = os.Rename(old, new); err == nil {
			return nil
		}
	}
	return err
}
