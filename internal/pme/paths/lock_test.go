package paths

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// bump reads a counter, yields, and writes it back plus one: lost updates show up as a low count.
func bump(path string) error {
	return WithLock(path, func() error {
		b, _ := os.ReadFile(path)
		n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		for i := 0; i < 100; i++ {
			os.Stat(path)
		}
		return WriteFileAtomic(path, []byte(strconv.Itoa(n+1)), 0o600)
	})
}

func TestWithLockSerializesGoroutinesAndProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "counter")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				if err := bump(path); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	exe, _ := os.Executable()
	var cmds []*exec.Cmd
	for i := 0; i < 6; i++ {
		cmd := exec.Command(exe, "-test.run=^TestHelperBump$")
		cmd.Env = append(os.Environ(), "P0SSESS_HELPER_BUMP="+path)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, cmd)
	}
	wg.Wait()
	for _, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(path)
	if got := strings.TrimSpace(string(b)); got != strconv.Itoa(20*5+6*10) {
		t.Errorf("counter = %s after %d locked increments", got, 20*5+6*10)
	}
	if ents, _ := os.ReadDir(filepath.Dir(path)); len(ents) != 1 {
		t.Errorf("lock left files beside the target: %v", ents)
	}
}

// TestHelperBump is the child process for TestWithLockSerializesGoroutinesAndProcesses.
func TestHelperBump(t *testing.T) {
	path := os.Getenv("P0SSESS_HELPER_BUMP")
	if path == "" {
		t.Skip("helper process")
	}
	for i := 0; i < 10; i++ {
		if err := bump(path); err != nil {
			t.Fatal(err)
		}
	}
}
