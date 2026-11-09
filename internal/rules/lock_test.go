package rules

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
)

func addForbid(f Files, n string) error {
	_, err := Add(f, Rule{Kind: KindForbid, Target: TargetModel, Models: []string{"x/m-" + n}}, now)
	return err
}

// TestConcurrentAddsAreAllKept adds rules to one file from goroutines and from separate processes. A
// lost rule here would be a lost forbid: a limit that silently stops applying.
func TestConcurrentAddsAreAllKept(t *testing.T) {
	f := files(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := addForbid(f, fmt.Sprint(i)); err != nil {
				t.Error(err)
			}
		}()
	}
	exe, _ := os.Executable()
	var cmds []*exec.Cmd
	for i := 0; i < 6; i++ {
		cmd := exec.Command(exe, "-test.run=^TestHelperAdd$")
		cmd.Env = append(os.Environ(), "P0SSESS_HELPER_RULES="+f.Global, fmt.Sprintf("P0SSESS_HELPER_N=%d", i))
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
	rs, err := ReadFile(f.Global, ScopeGlobal)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 20+6*5 {
		t.Fatalf("%d rules kept after %d concurrent adds", len(rs), 20+6*5)
	}
	var wg2 sync.WaitGroup
	for _, r := range rs[:10] {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			if _, err := Remove(f, r.ID); err != nil {
				t.Error(err)
			}
		}()
	}
	wg2.Wait()
	if rs, _ = ReadFile(f.Global, ScopeGlobal); len(rs) != 40 {
		t.Errorf("%d rules left after 10 concurrent removes of 50", len(rs))
	}
}

// TestHelperAdd is the child process for TestConcurrentAddsAreAllKept.
func TestHelperAdd(t *testing.T) {
	path := os.Getenv("P0SSESS_HELPER_RULES")
	if path == "" {
		t.Skip("helper process")
	}
	for j := 0; j < 5; j++ {
		if err := addForbid(Files{Global: path}, os.Getenv("P0SSESS_HELPER_N")+"-"+fmt.Sprint(j)); err != nil {
			t.Fatal(err)
		}
	}
}
