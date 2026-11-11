package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func plugin(t *testing.T, version, versionFile, sums string) string {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".claude-plugin"), 0o755)
	os.MkdirAll(filepath.Join(root, "dist"), 0o755)
	os.WriteFile(filepath.Join(root, ".claude-plugin", "plugin.json"), []byte(`{"name":"p0ssess","version":"`+version+`"}`), 0o644)
	os.WriteFile(filepath.Join(root, "dist", "VERSION"), []byte(versionFile), 0o644)
	os.WriteFile(filepath.Join(root, "dist", "SHA256SUMS"), []byte(sums), 0o644)
	return root
}

func TestWant(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	name := "p0ssess-windows-amd64.exe"
	// A Windows checkout may turn the line ends into CRLF.
	root := plugin(t, "1.2.3", "1.2.3\r\ngo1.27.3\r\n", sum+"  p0ssess-linux-amd64\r\n"+sum+" *"+name+"\r\n")
	v, err := pluginVersion(root)
	if err != nil || v != "1.2.3" {
		t.Fatalf("pluginVersion = %q, %v", v, err)
	}
	l := &launch{root: root, version: v, name: name}
	if got, ok := l.want(); !ok || got != sum {
		t.Errorf("want = %q, %v", got, ok)
	}
	l.version = "1.2.4"
	if _, ok := l.want(); ok {
		t.Error("sums for another version were used")
	}
	l.version, l.name = "1.2.3", "p0ssess-darwin-arm64"
	if _, ok := l.want(); ok {
		t.Error("a binary with no sum got one")
	}
}

func TestInstallCheckedRefusesMismatch(t *testing.T) {
	root := plugin(t, "1.0.0", "1.0.0\n", strings.Repeat("0", 64)+"  p0ssess-test\n")
	src := filepath.Join(root, "dist", "p0ssess-test")
	os.WriteFile(src, []byte("not the binary"), 0o755)
	l := &launch{root: root, version: "1.0.0", name: "p0ssess-test", bin: filepath.Join(t.TempDir(), "p0ssess-test")}
	if l.installChecked(src, "dist/p0ssess-test") || isFile(l.bin) {
		t.Fatal("a file with the wrong checksum was installed")
	}
}
