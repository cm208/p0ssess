package jail

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"p0ssess/internal/config"
)

func TestPoliciesHardDeny(t *testing.T) {
	ps := Policies()
	has := func(action, resource, effect string) bool {
		return slices.Contains(ps, policy{action, resource, effect})
	}
	for _, s := range []string{"sudo", "sudo *", "su", "su *", "ssh *", "scp *", "git push *", "git * push *",
		"docker *", "podman *", "systemctl *", "sh", "sh *", "bash", "bash -*c*"} {
		if !has("tool.use", "shell:"+s, "deny") {
			t.Errorf("missing shell deny %q", s)
		}
	}
	for _, f := range []string{".ssh/*", ".git/*", ".git", ".env", ".env.*"} {
		for _, form := range []string{f, "*/" + f} {
			if !has("tool.use", "edit:"+form, "deny") {
				t.Errorf("missing edit deny %q", form)
			}
		}
	}
	for _, f := range []string{".ssh/*", "*/.ssh/*", ".env", "*/.env"} {
		if !has("tool.use", "read:"+f, "deny") {
			t.Errorf("missing read deny %q", f)
		}
	}
	if !has("provider.use", "*", "deny") || !has("provider.use", "openrouter", "allow") || !has("tool.use", "external_directory:*", "deny") {
		t.Error("provider or external_directory policy missing")
	}
	// Reads of absolute paths (always outside the worktree) are denied, /proc and /state among them.
	for _, r := range []string{"read:/*", "read:/proc/*", "read:/state/*", "read:/state"} {
		if !has("tool.use", r, "deny") {
			t.Errorf("missing %q deny", r)
		}
	}
	// Webfetch to the loopback API, the proxy and private ranges is denied.
	for _, r := range []string{"webfetch:http://127.*", "webfetch:https://127.*", "webfetch:http://10.*",
		"webfetch:http://192.168.*", "webfetch:http://localhost*"} {
		if !has("tool.use", r, "deny") {
			t.Errorf("missing %q deny", r)
		}
	}
	// The provider allow must come after the provider deny: the last match wins.
	if slices.Index(ps, policy{"provider.use", "openrouter", "allow"}) < slices.Index(ps, policy{"provider.use", "*", "deny"}) {
		t.Error("provider allow precedes the deny")
	}
}

func TestOpenCodeConfig(t *testing.T) {
	b, err := OpenCodeConfig()
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	or := cfg["providers"].(map[string]any)["openrouter"].(map[string]any)
	if or["settings"].(map[string]any)["baseURL"] != "http://"+ProxyAddr+"/api/v1" {
		t.Errorf("baseURL: %v", or["settings"])
	}
	if or["body"].(map[string]any)["provider"].(map[string]any)["data_collection"] != "deny" {
		t.Error("data_collection not deny")
	}
	if cfg["share"] != "disabled" {
		t.Error("share not disabled")
	}
	if cfg["shell"] != ServerExePath {
		t.Errorf("shell is %v, want %s", cfg["shell"], ServerExePath)
	}
	if cfg["formatter"] != false {
		t.Error("formatter not disabled")
	}
	if cfg["lsp"] != false {
		t.Error("lsp not disabled")
	}
	if strings.Contains(string(b), "sk-or-") {
		t.Error("config contains a key")
	}
}

func TestNftRules(t *testing.T) {
	r := NftRules([]string{"203.0.113.0/24"}, []string{"2001:db8::/64"})
	for _, want := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16",
		"fc00::/7", "fd00::/8", "fd7a:115c:a1e0::/48", "fe80::/10", "203.0.113.0/24", "2001:db8::/64",
		"oifname \"lo\" accept", "ip daddr 169.254.1.1 meta l4proto { tcp, udp } th dport 53 accept", "table inet " + NftTable} {
		if !strings.Contains(r, want) {
			t.Errorf("rules lack %q", want)
		}
	}
	if strings.Index(r, "169.254.1.1") > strings.Index(r, "169.254.0.0/16") {
		t.Error("the DNS accept must come before the link-local reject")
	}
}

func TestNewName(t *testing.T) {
	a, b := newName(), newName()
	if a == b || !strings.HasPrefix(a, "p0ssess-") || len(a) != len("p0ssess-")+12 {
		t.Errorf("%s %s", a, b)
	}
	if _, err := Load("../etc"); err == nil {
		t.Error("Load accepted a path")
	}
}

func TestStale(t *testing.T) {
	now := time.Now()
	self := &Jail{Owner: Self()}
	if self.Stale(now) {
		t.Error("a jail owned by this process is stale")
	}
	// A pid that exists but started at another time is not the owner.
	reused := &Jail{Owner: Owner{PID: os.Getpid(), Start: Self().Start + 1}}
	if !reused.Stale(now) {
		t.Error("a reused pid kept the jail")
	}
	dead := &Jail{Owner: Owner{PID: 1 << 30}}
	if !dead.Stale(now) {
		t.Error("a jail whose owner is gone is not stale")
	}
	detached := &Jail{Owner: Owner{PID: 1 << 30}, Detached: true, KeepUntil: now.Add(time.Minute)}
	if detached.Stale(now) {
		t.Error("a detached jail was stale before its keep time")
	}
	if !detached.Stale(now.Add(2 * time.Minute)) {
		t.Error("a detached jail outlived its keep time")
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
}

// A run started from the home directory mounted all of it into the jail (0.5.3 and earlier). Only the
// top of a git work tree with no credential directories, apart from p0ssess's own files, may be mounted.
func TestCheckMountable(t *testing.T) {
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(tmp, "home", "user")
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	t.Setenv("HOME", home)
	t.Setenv("P0SSESS_HOME", filepath.Join(tmp, "p0home"))
	key := filepath.Join(tmp, "keys", "openrouter_key")

	repo := filepath.Join(home, "src", "app")
	gitInit(t, repo)
	os.MkdirAll(filepath.Join(repo, "pkg"), 0o755)
	if err := CheckMountable(repo, key); err != nil {
		t.Fatalf("a plain repository: %v", err)
	}
	plain := filepath.Join(home, "notes")
	os.MkdirAll(plain, 0o755)
	withSSH := filepath.Join(tmp, "withssh")
	gitInit(t, withSSH)
	os.MkdirAll(filepath.Join(withSSH, ".ssh"), 0o700)
	withConfig := filepath.Join(tmp, "withconfig")
	gitInit(t, withConfig)
	os.MkdirAll(filepath.Join(withConfig, ".config"), 0o700)
	holdsState := filepath.Join(tmp, "holdsstate")
	gitInit(t, holdsState)
	holdsKey := filepath.Join(tmp, "keys")
	gitInit(t, holdsKey)
	t.Run("p0ssess home inside a repository", func(t *testing.T) {
		t.Setenv("P0SSESS_HOME", filepath.Join(holdsState, "p0"))
		if err := CheckMountable(holdsState, key); err == nil {
			t.Error("mounted a repository holding p0ssess's state")
		}
	})
	for name, dir := range map[string]string{
		"home": home, "home's parent": filepath.Dir(home), "root": "/", "not a repository": plain,
		"below the top": filepath.Join(repo, "pkg"), ".ssh at the top": withSSH, ".config at the top": withConfig,
		"holds the key file": holdsKey,
	} {
		var nm *NotMountable
		if err := CheckMountable(dir, key); !errors.As(err, &nm) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A home directory that is itself a git work tree (dotfiles) is still the home directory.
	gitInit(t, home)
	if err := CheckMountable(home, key); err == nil || !strings.Contains(err.Error(), "home directory") {
		t.Errorf("home as a repository: %v", err)
	}
	if _, err := RepoRoot(plain); err != nil {
		t.Errorf("inside home's repository now: %v", err)
	}
	// Start checks before it reads the key or touches podman.
	_, err := Start(context.Background(), Spec{Repo: home}, config.Config{OpenRouterKeyFile: key}, io.Discard)
	var nm *NotMountable
	if !errors.As(err, &nm) || !strings.Contains(err.Error(), "Pass cwd") {
		t.Errorf("Start(home): %v", err)
	}
}

func TestRepoRootRefusesOutsideGit(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_DIR", filepath.Join(dir, "elsewhere"))
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	if _, err := RepoRoot(dir); err == nil || !strings.Contains(err.Error(), "not inside a git work tree") {
		t.Errorf("err = %v", err)
	}
}

func TestCleanLog(t *testing.T) {
	got := cleanLog("line one\nauth pw-123 failed\nkey sk-or-v1-abcDEF_09 used\n", "pw-123")
	if strings.Contains(got, "pw-123") || strings.Contains(got, "sk-or-") || !strings.Contains(got, "line one | auth [password] failed | key [key] used") {
		t.Errorf("cleanLog = %q", got)
	}
	if long := cleanLog(strings.Repeat("x", 2000), ""); len(long) > 610 {
		t.Errorf("not truncated: %d", len(long))
	}
}
