package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "cc"))
	t.Setenv("P0SSESS_HOME", filepath.Join(dir, "p0"))
	orig := "{\n  \"model\": \"opus\",\n  \"statusLine\": {\n    \"type\": \"command\",\n    \"command\": \"echo mine\"\n  },\n  \"permissions\": {\n    \"allow\": [\n      \"Bash(ls:*)\"\n    ]\n  }\n}\n"
	os.MkdirAll(filepath.Join(dir, "cc"), 0o700)
	p := filepath.Join(dir, "cc", "settings.json")
	os.WriteFile(p, []byte(orig), 0o600)
	for _, args := range [][]string{{"allow"}, {"allow"}, {"statusline"}, {"summaries"}} {
		if err := setupCmd(args); err != nil {
			t.Fatal(args, err)
		}
	}
	b, _ := os.ReadFile(p)
	s := string(b)
	if strings.Count(s, toolPrefix+"run\"") != 1 || !strings.Contains(s, "p0ssess\\\" statusline") || !strings.Contains(s, summariesEnv) {
		t.Fatalf("after setup:\n%s", s)
	}
	if strings.Index(s, "\"model\"") > strings.Index(s, "\"permissions\"") {
		t.Error("key order changed")
	}
	if err := setupCmd([]string{"undo"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != orig {
		t.Errorf("undo did not restore the file:\n%s", b)
	}
	bks, _ := os.ReadDir(filepath.Join(dir, "p0", "backups"))
	if len(bks) != 4 {
		t.Errorf("backups = %d, want 4", len(bks))
	}
}
