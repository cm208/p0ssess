package relay

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"p0ssess/internal/config"
	"p0ssess/internal/jail"
)

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
}

func gitInit(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	for name, body := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "init", "--allow-empty"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

// testHome makes a home directory with two repositories under ~/claude and a notes folder.
func testHome(t *testing.T) (home, cloud, client string) {
	t.Helper()
	needGit(t)
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	home = filepath.Join(tmp, "home")
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	os.MkdirAll(filepath.Join(home, "notes"), 0o755)
	os.WriteFile(filepath.Join(home, "notes", "todo.txt"), []byte("buy milk\n"), 0o644)
	t.Setenv("HOME", home)
	t.Setenv("P0SSESS_HOME", filepath.Join(tmp, "p0"))
	t.Setenv("GIT_CEILING_DIRECTORIES", tmp)
	cloud = filepath.Join(home, "claude", "p0ssess-cloud")
	client = filepath.Join(home, "claude", "p0ssess")
	gitInit(t, client, map[string]string{"go.mod": "module x\n", "main.go": "package main\n"})
	gitInit(t, cloud, map[string]string{"ops/SPEC.md": "Client code lives in " + client + "/internal; server code here.\n"})
	return home, cloud, client
}

// The target repository comes from the paths a task names, a spec file it names, or a repository name
// near the caller; never from a refusal (1.0.4 items 3a, 13 and D).
func TestResolve(t *testing.T) {
	home, cloud, client := testHome(t)
	key := filepath.Join(home, "key")

	tg := Resolve("fix the build in "+client+"/main.go", cloud, false, key, nil)
	if tg.Repo != client || !tg.Named || tg.Cwd != client || len(tg.Notes) == 0 || !strings.Contains(tg.Notes[0], client) {
		t.Errorf("named path: %+v", tg)
	}
	// The trial of 2026-11-17: the spec in the cloud repository names the client repository; both mount.
	tg = Resolve("build the spec in ~/claude/p0ssess-cloud/ops/SPEC.md", cloud, false, key, nil)
	if tg.Repo != cloud || len(tg.Extra) != 1 || tg.Extra[0] != client {
		t.Errorf("spec: %+v", tg)
	}
	// A caller's explicit cwd wins as the start, and the named repository is mounted too.
	tg = Resolve("compare with "+client+"/main.go", cloud, true, key, nil)
	if tg.Repo != cloud || len(tg.Extra) != 1 || tg.Extra[0] != client {
		t.Errorf("explicit: %+v", tg)
	}
	// From the home directory, a repository named in the task is picked, and the pick is said.
	tg = Resolve("fix the parser in the p0ssess repo", home, false, key, nil)
	if tg.Repo != client || len(tg.Notes) == 0 || !strings.Contains(tg.Notes[0], "picked") {
		t.Errorf("by name: %+v", tg)
	}
	// Nothing fits: no repository, and files outside every repository are listed for the direct path.
	tg = Resolve("summarize ~/notes/todo.txt", home, false, key, nil)
	if tg.Repo != "" || len(tg.Files) != 1 || tg.Files[0] != filepath.Join(home, "notes", "todo.txt") {
		t.Errorf("no repo: %+v", tg)
	}
	if ps := taskPaths(`see "/a/b.go", ~/x/y.md. and https://example.com/z`); len(ps) < 2 || ps[0] != "/a/b.go" || ps[1] != filepath.Join(home, "x/y.md") {
		t.Errorf("paths %v", ps)
	}
}

func fakeDirect(got *[]DirectTask) DirectRunner {
	return func(ctx context.Context, d DirectTask) (*Result, error) {
		*got = append(*got, d)
		return &Result{Answer: "answer", Status: "done", Model: "google/gemini-3.8-flash", CostUSD: 0.002}, nil
	}
}

// Read-only work from a directory that is no repository goes to the direct path, with the files it
// names; nothing is refused and no jail starts (1.0.4 acceptance: non-git cwd).
func TestReadFromNonGitCwdGoesDirect(t *testing.T) {
	home, _, _ := testHome(t)
	e := NewEngine(config.Config{MaxRunCostUSD: 0.5}, "test", io.Discard)
	e.Unguarded = true
	var got []DirectTask
	e.SetDirect(fakeDirect(&got))
	e.RunHook = func(context.Context, RunRequest) (*Result, error) {
		t.Fatal("the agent path was used")
		return nil, nil
	}
	res, err := e.Do(context.Background(), RunRequest{Task: "summarize ~/notes/todo.txt for me", Cwd: home}, nil)
	if err != nil || res.Answer != "answer" || res.Route != "direct" || len(got) != 1 || got[0].Web {
		t.Fatalf("%+v %v %+v", res, err, got)
	}
	if len(got[0].Files) != 1 || !strings.HasSuffix(got[0].Files[0], "todo.txt") {
		t.Errorf("files %v", got[0].Files)
	}
	if res.Receipt != "(gemini-3.8-flash, $0.0020)" {
		t.Errorf("receipt %q", res.Receipt)
	}
}

// A task that needs current information goes to the web-capable direct path; without one it says so
// before anything is spent (1.0.4 item 3c).
func TestCurrentInfoRoutesToWeb(t *testing.T) {
	_, _, client := testHome(t)
	e := NewEngine(config.Config{MaxRunCostUSD: 0.5}, "test", io.Discard)
	e.Unguarded = true
	var got []DirectTask
	e.SetDirect(fakeDirect(&got))
	ran := false
	e.RunHook = func(context.Context, RunRequest) (*Result, error) { ran = true; return &Result{Status: "done"}, nil }
	task := "brief market trend insight from news headlines of the past 6 months about AI users who don't code"
	res, err := e.Do(context.Background(), RunRequest{Task: task, Cwd: client}, nil)
	if err != nil || ran || len(got) != 1 || !got[0].Web || res.Route != "direct" {
		t.Fatalf("%+v %v %+v ran=%v", res, err, got, ran)
	}

	// No web-capable route: one plain line before spending, with the retry-through-p0ssess instruction.
	e.SetDirect(nil)
	_, err = e.Do(context.Background(), RunRequest{Task: task, Cwd: client}, nil)
	if err == nil || ran || !strings.Contains(err.Error(), "needs current information") || !strings.Contains(err.Error(), "nothing was run") ||
		!strings.Contains(err.Error(), "send the request to the p0ssess agent again") {
		t.Errorf("%v ran=%v", err, ran)
	}

	// An edit task gathers the web data first and hands it to the sandboxed run.
	e.SetDirect(fakeDirect(&got))
	var sent RunRequest
	e.RunHook = func(_ context.Context, r RunRequest) (*Result, error) {
		sent = r
		return &Result{Status: "done", Mode: "edit", Changed: "1 file(s) changed, +1 -0"}, nil
	}
	res, err = e.Do(context.Background(), RunRequest{Task: "update " + client + "/main.go with this week's exchange rates", Mode: "edit", Model: "z-ai/glm-5.3-flash"}, nil)
	if err != nil || !strings.Contains(sent.Task, "Current information p0ssess gathered") || !got[len(got)-1].Web {
		t.Errorf("%+v %v %q", res, err, sent.Task)
	}
}

// Failures p0ssess can fix are retried inside it with a log line each; the rest become one plain
// sentence (1.0.4 items C, 12; 1.1 decision 6).
func TestDoSelfHeals(t *testing.T) {
	_, _, client := testHome(t)
	e := NewEngine(config.Config{MaxRunCostUSD: 0.5}, "test", io.Discard)
	e.Unguarded = true
	e.SetObserver(&fakeChooser{out: Choice{Model: "google/gemini-4-carbon", UseCase: "repo-scan-explore", Stakes: 2, Why: "best",
		Alternatives: []string{"deepseek/deepseek-v4.1-flash"}}})
	var reqs []RunRequest
	built := 0
	e.BuildImage = func(context.Context) error { built++; return nil }
	steps := []func(RunRequest) (*Result, error){
		func(RunRequest) (*Result, error) { return nil, &jail.MissingImage{Image: "x"} },
		func(RunRequest) (*Result, error) {
			return &Result{Status: "failed", Error: "Model unavailable: openrouter/google/gemini-4-carbon", SessionID: ""}, nil
		},
		func(RunRequest) (*Result, error) {
			return nil, errors.New("openrouter: HTTP 402: This request requires more credits, or fewer max_tokens. You requested up to 64000 tokens, but can only afford 44791")
		},
		func(RunRequest) (*Result, error) {
			return &Result{Status: "done", Answer: "found it", Model: "deepseek/deepseek-v4.1-flash"}, nil
		},
	}
	e.RunHook = func(_ context.Context, r RunRequest) (*Result, error) {
		reqs = append(reqs, r)
		return steps[len(reqs)-1](r)
	}
	res, err := e.Do(context.Background(), RunRequest{Task: "where is the parser defined", Cwd: client}, nil)
	if err != nil || res.Answer != "found it" || built != 1 || len(reqs) != 4 {
		t.Fatalf("%+v %v built=%d runs=%d", res, err, built, len(reqs))
	}
	if reqs[2].Model != "deepseek/deepseek-v4.1-flash" || reqs[3].MaxOutput != 44791*9/10 || len(reqs[0].Alternatives) != 1 {
		t.Errorf("requests %+v", reqs)
	}
	log := strings.Join(res.Retries, "\n")
	for _, want := range []string{"built it", "switched to deepseek-v4.1-flash", "smaller output limit"} {
		if !strings.Contains(log, want) {
			t.Errorf("retry log lacks %q:\n%s", want, log)
		}
	}

	// Damaged podman storage: one plain line naming the fix, warning what --repair deletes.
	e.RunHook = func(context.Context, RunRequest) (*Result, error) {
		return nil, errors.New("jail: podman run: exit status 125: Error: creating container storage: creating an ID-mapped copy of layer \"3f2a\": no such file or directory")
	}
	_, err = e.Do(context.Background(), RunRequest{Task: "where is main", Cwd: client, Model: "z-ai/glm-5.3-flash"}, nil)
	if err == nil || !strings.Contains(err.Error(), "p0ssess jail build") || !strings.Contains(err.Error(), "deletes every image") ||
		strings.Contains(err.Error(), "exit status") {
		t.Errorf("storage: %v", err)
	}
	// Anything raw is replaced by a plain sentence and logged.
	e.RunHook = func(context.Context, RunRequest) (*Result, error) {
		return nil, errors.New("jail: server did not come up: dial tcp 127.0.0.1:41000: connection refused")
	}
	_, err = e.Do(context.Background(), RunRequest{Task: "where is main", Cwd: client, Model: "z-ai/glm-5.3-flash"}, nil)
	if err == nil || strings.Contains(err.Error(), "dial tcp") || !strings.Contains(err.Error(), "errors.log") {
		t.Errorf("raw: %v", err)
	}
	// A key OpenRouter rejects (text seen in the gate run of 2026-11-21) names the one fix.
	if got := Plain(errors.New("openrouter: HTTP 401: User not found.")).Error(); !strings.Contains(got, "did not accept your API key") {
		t.Errorf("key: %v", got)
	}
	// Decisions for the user keep their full text.
	appr := errors.New(`needs approval: about $0.42 on openai/gpt-6.1-sol, above your $0.25 auto-approve limit; approvals_add {"model":"openai/gpt-6.1-sol"}`)
	if got := Plain(appr); !strings.Contains(got.Error(), `approvals_add {"model":"openai/gpt-6.1-sol"}`) {
		t.Errorf("approval: %v", got)
	}
}

// The caller's uncommitted changes are in the agent's copy, and the run's diff holds only its own
// changes (1.0.4 item 8).
func TestEditCopyCarriesDirtyTree(t *testing.T) {
	_, _, client := testHome(t)
	os.WriteFile(filepath.Join(client, "main.go"), []byte("package main\n\nfunc half() {}\n"), 0o644)
	os.WriteFile(filepath.Join(client, "new.go"), []byte("package main\n"), 0o644)
	e := NewEngine(config.Config{}, "test", io.Discard)
	wt, base, head, n, err := e.editCopy(client)
	if err != nil || n != 2 || base == head {
		t.Fatalf("%s %s %s %d %v", wt, base, head, n, err)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "main.go")); !strings.Contains(string(b), "half") {
		t.Error("tracked change missing from the copy")
	}
	if _, err := os.Stat(filepath.Join(wt, "new.go")); err != nil {
		t.Error("untracked file missing from the copy")
	}
	os.WriteFile(filepath.Join(wt, "model.go"), []byte("package main\n"), 0o644)
	stat, patch, err := worktreeDiff(client, wt, base)
	if err != nil || !strings.Contains(stat, "model.go") || strings.Contains(patch, "half") || strings.Contains(stat, "new.go") {
		t.Errorf("diff %q %v", stat, err)
	}
}

// An edit run's changes are checked, tested and applied to a clean checkout; a dirty one gets the apply
// line; a pass that changed nothing is retried once (1.0.4 item F).
func TestAfterEdit(t *testing.T) {
	_, _, client := testHome(t)
	e := NewEngine(config.Config{}, "test", io.Discard)
	tested := 0
	e.TestRunner = func(_ context.Context, dir string, tc jail.Toolchain, _ []jail.Toolchain) jail.TestRun {
		tested++
		if !strings.Contains(dir, ".p0ssess/wt") || tc.Kind != "go" {
			t.Errorf("tests in %s for %s", dir, tc.Kind)
		}
		return jail.TestRun{Command: tc.Test, Passed: true}
	}
	start := func() (*SessionRecord, *Result) {
		wt, base, head, n, err := e.editCopy(client)
		if err != nil {
			t.Fatal(err)
		}
		rec := &SessionRecord{SessionID: "ses_" + filepath.Base(wt), Repo: client, Cwd: client, Location: wt, Mode: "edit",
			Worktree: wt, BaseRev: base, HeadAtStart: head, Carried: n, Toolchains: []jail.Toolchain{{Kind: "go", Test: "go test ./..."}}}
		rec.save()
		return rec, &Result{Status: "done", Mode: "edit", SessionID: rec.SessionID}
	}
	if _, err := exec.LookPath("go"); err != nil {
		jail.SetLookTool(func(string) (string, error) { return "/usr/bin/true", nil }, func(string, ...string) (string, error) { return "/usr/lib/go\n", nil })
		defer jail.SetLookTool(nil, nil)
	}

	rec, res := start()
	os.WriteFile(filepath.Join(rec.Worktree, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644)
	e.collectDiffs(res, rec)
	res, log := e.afterEdit(context.Background(), RunRequest{}, res, nil, nil)
	b, _ := os.ReadFile(filepath.Join(client, "main.go"))
	if len(res.Applied) != 1 || res.Apply != "" || !strings.Contains(res.Revert, "apply -R") || !strings.Contains(string(b), "func main") {
		t.Fatalf("clean: %+v %v", res, log)
	}
	if tested != 1 || !strings.Contains(res.Tests, "passed") {
		t.Errorf("tests %q (%d)", res.Tests, tested)
	}
	exec.Command("git", "-C", client, "-c", "user.name=t", "-c", "user.email=t@e", "commit", "-qam", "applied").Run()

	// A dirty checkout gets the apply line instead.
	os.WriteFile(filepath.Join(client, "wip.txt"), []byte("mine\n"), 0o644)
	rec, res = start()
	os.WriteFile(filepath.Join(rec.Worktree, "extra.go"), []byte("package main\n"), 0o644)
	e.collectDiffs(res, rec)
	res, log = e.afterEdit(context.Background(), RunRequest{}, res, nil, nil)
	if len(res.Applied) != 0 || !strings.Contains(res.Apply, "git -C") || !strings.Contains(strings.Join(log, " "), "uncommitted") {
		t.Errorf("dirty: %+v %v", res, log)
	}
	os.Remove(filepath.Join(client, "wip.txt"))

	// Nothing changed: one more pass in the same session, then the result says so.
	rec, res = start()
	e.collectDiffs(res, rec)
	asked := ""
	e.RunHook = func(_ context.Context, r RunRequest) (*Result, error) {
		asked = r.Task
		os.WriteFile(filepath.Join(rec.Worktree, "late.go"), []byte("package main\n"), 0o644)
		r2 := &Result{Status: "done", Mode: "edit", SessionID: rec.SessionID}
		e.collectDiffs(r2, rec)
		return r2, nil
	}
	res, log = e.afterEdit(context.Background(), RunRequest{}, res, nil, nil)
	if r := res; !strings.Contains(asked, "Nothing changed") || r.Changed == "no changes" || !strings.Contains(strings.Join(log, " "), "asked once more") {
		t.Errorf("no change: %+v %v %q", r, log, asked)
	}
}

// The scratch repository holds copies of the named files and is a real repository with one commit.
func TestScratchRepo(t *testing.T) {
	home, _, _ := testHome(t)
	f := filepath.Join(home, "notes", "todo.txt")
	dir, copies, err := scratchRepo([]string{f, filepath.Join(home, ".ssh")})
	if err != nil || len(copies) != 1 {
		t.Fatalf("%v %v", copies, err)
	}
	defer os.RemoveAll(dir)
	if b, _ := os.ReadFile(copies[f]); string(b) != "buy milk\n" {
		t.Errorf("copy %q", b)
	}
	if _, err := git(dir, "rev-parse", "HEAD"); err != nil {
		t.Error(err)
	}
	if jail.CheckMountable(dir, "") != nil {
		t.Errorf("scratch not mountable: %v", jail.CheckMountable(dir, ""))
	}
}

// Without the sandbox (no podman), read-only work still runs, on the direct path with the files it
// names; edit work says why it cannot run.
func TestNoSandboxReadGoesDirect(t *testing.T) {
	_, _, client := testHome(t)
	e := NewEngine(config.Config{MaxRunCostUSD: 0.5}, "test", io.Discard)
	e.Unguarded = true
	e.SandboxCheck = func() error { return errors.New("podman is not installed") }
	var got []DirectTask
	e.SetDirect(fakeDirect(&got))
	res, err := e.Do(context.Background(), RunRequest{Task: "explain " + client + "/main.go"}, nil)
	if err != nil || res.Route != "direct" || len(got) != 1 || len(got[0].Files) != 1 {
		t.Fatalf("%+v %v %+v", res, err, got)
	}
	_, err = e.Do(context.Background(), RunRequest{Task: "fix " + client + "/main.go", Mode: "edit"}, nil)
	if err == nil || !strings.Contains(err.Error(), "rootless podman") {
		t.Errorf("edit: %v", err)
	}
}

// A Free install without podman gets the one plain line for edit work, with no wrapper around it.
func TestNoSandboxFreeLine(t *testing.T) {
	_, _, client := testHome(t)
	e := NewEngine(config.Config{MaxRunCostUSD: 0.5}, "test", io.Discard)
	e.Unguarded = true
	e.SandboxCheck = func() error { return &jail.NeedsPro{GOOS: "darwin"} }
	_, err := e.Do(context.Background(), RunRequest{Task: "fix " + client + "/main.go", Mode: "edit"}, nil)
	want := "This job needs a sandbox, and it can run in the p0ssess cloud sandbox with Pro (/p0ssess:pro). " +
		"For now, read-only questions still run on the direct path, or use Linux with rootless podman."
	if err == nil || !strings.HasPrefix(err.Error(), want) || strings.Contains(err.Error(), "unavailable here") {
		t.Errorf("%v", err)
	}
}

// A model p0ssess picked that needs approval gives way to the next option inside the limits.
func TestApprovalFallsBackInsideLimits(t *testing.T) {
	_, _, client := testHome(t)
	e := NewEngine(config.Config{MaxRunCostUSD: 0.5}, "test", io.Discard)
	e.Unguarded = true
	e.SetObserver(&fakeChooser{out: Choice{Model: "anthropic/claude-opus-5.5", Why: "best", Alternatives: []string{"moonshotai/kimi-k3.1-pro"}}})
	var models []string
	e.RunHook = func(_ context.Context, r RunRequest) (*Result, error) {
		models = append(models, r.Model)
		if len(models) == 1 {
			return nil, errors.New("needs approval: about $0.21 on anthropic/claude-opus-5.5, above your $0.10 auto-approve limit")
		}
		return &Result{Status: "done", Answer: "ok", Model: r.Model}, nil
	}
	res, err := e.Do(context.Background(), RunRequest{Task: "where is main defined", Cwd: client}, nil)
	if err != nil || len(models) != 2 || models[1] != "moonshotai/kimi-k3.1-pro" || !strings.Contains(strings.Join(res.Retries, " "), "inside it") {
		t.Errorf("%+v %v %v", res, err, models)
	}
}

// A scratch edit run's changed copies go back over the originals that did not change meanwhile; the
// originals are kept for the revert line, and a file changed during the run gets the copy line instead.
func TestScratchCopyBack(t *testing.T) {
	home, _, _ := testHome(t)
	todo := filepath.Join(home, "notes", "todo.txt")
	other := filepath.Join(home, "notes", "plan.txt")
	os.WriteFile(other, []byte("plan\n"), 0o600)
	dir, copies, err := scratchRepo([]string{todo, other})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	e := NewEngine(config.Config{}, "test", io.Discard)
	wt, base, head, _, err := e.editCopy(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := &SessionRecord{SessionID: "ses_scratch", Repo: dir, Cwd: dir, Location: wt, Mode: "edit", Worktree: wt, BaseRev: base,
		HeadAtStart: head, Scratch: true, Copies: copies}
	rec.save()
	os.WriteFile(filepath.Join(wt, "todo.txt"), []byte("buy milk\nbuy eggs\n"), 0o644)
	os.WriteFile(filepath.Join(wt, "plan.txt"), []byte("new plan\n"), 0o644)
	os.WriteFile(other, []byte("plan, edited by hand\n"), 0o600)
	res := &Result{Status: "done", Mode: "edit", SessionID: rec.SessionID}
	e.collectDiffs(res, rec)
	res, log := e.afterEdit(context.Background(), RunRequest{}, res, nil, nil)
	if b, _ := os.ReadFile(todo); string(b) != "buy milk\nbuy eggs\n" {
		t.Errorf("todo not copied back: %q %+v %v", b, res, log)
	}
	if b, _ := os.ReadFile(other); string(b) != "plan, edited by hand\n" {
		t.Errorf("a file changed during the run was overwritten: %q", b)
	}
	if len(res.Applied) != 1 || !strings.Contains(res.Apply, "plan.txt") || !strings.Contains(strings.Join(log, " "), "changed during the run") {
		t.Errorf("%+v %v", res, log)
	}
	if st, _ := os.Stat(todo); st.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", st.Mode())
	}
	if out, err := exec.Command("sh", "-c", res.Revert).CombinedOutput(); err != nil {
		t.Fatalf("revert: %v %s", err, out)
	}
	if b, _ := os.ReadFile(todo); string(b) != "buy milk\n" {
		t.Errorf("revert: %q", b)
	}
	// apply_edits = "never" leaves every file alone.
	e2 := NewEngine(config.Config{ApplyEdits: "never"}, "test", io.Discard)
	res = &Result{Status: "done", Mode: "edit", SessionID: rec.SessionID}
	e2.collectDiffs(res, rec)
	res, _ = e2.afterEdit(context.Background(), RunRequest{}, res, nil, nil)
	if b, _ := os.ReadFile(todo); string(b) != "buy milk\n" || len(res.Applied) != 0 {
		t.Errorf("never: %q %+v", b, res)
	}
}

// A scratch task that names a directory for a new file gets it there (gate run of 2026-11-21: the
// model wrote m5gate/nongit/haiku.txt inside the copy, and nothing reached /tmp/m5gate/nongit).
func TestScratchNewFilesGoToTheNamedDirectory(t *testing.T) {
	home, _, _ := testHome(t)
	dest := filepath.Join(home, "poems")
	os.MkdirAll(dest, 0o755)
	os.WriteFile(filepath.Join(dest, "taken.txt"), []byte("mine\n"), 0o644)
	task := "Write a file named haiku.txt in " + dest + " with a haiku about rain."
	tg := Resolve(task, home, false, "", nil)
	if tg.Repo != "" || tg.Dest != dest {
		t.Fatalf("target %+v", tg)
	}
	dir, copies, err := scratchRepo(tg.Files)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	e := NewEngine(config.Config{}, "test", io.Discard)
	wt, base, head, _, err := e.editCopy(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := &SessionRecord{SessionID: "ses_scratch_new", Repo: dir, Cwd: dir, Location: wt, Mode: "edit", Worktree: wt, BaseRev: base,
		HeadAtStart: head, Scratch: true, Copies: copies, Dest: tg.Dest}
	rec.save()
	if p := orient(rec, nil, task); !strings.Contains(p, "haiku.txt in "+wt+" with") {
		t.Errorf("the named directory is not the copy's root in the prompt:\n%s", p)
	}
	os.WriteFile(filepath.Join(wt, "haiku.txt"), []byte("rain\n"), 0o644)
	os.WriteFile(filepath.Join(wt, "taken.txt"), []byte("theirs\n"), 0o644)
	res := &Result{Status: "done", Mode: "edit", SessionID: rec.SessionID}
	e.collectDiffs(res, rec)
	res, log := e.afterEdit(context.Background(), RunRequest{}, res, nil, nil)
	if b, _ := os.ReadFile(filepath.Join(dest, "haiku.txt")); string(b) != "rain\n" {
		t.Errorf("new file not placed: %q %+v %v", b, res, log)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "taken.txt")); string(b) != "mine\n" {
		t.Errorf("an existing file was overwritten: %q", b)
	}
	if !strings.Contains(res.Apply, "taken.txt") || !strings.Contains(strings.Join(log, " "), "a file is already there") {
		t.Errorf("%+v %v", res, log)
	}
	if out, err := exec.Command("sh", "-c", res.Revert).CombinedOutput(); err != nil {
		t.Fatalf("revert: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dest, "haiku.txt")); err == nil {
		t.Error("revert left the new file")
	}
}

// The command line runs Do and then Settle on the same result; its receipt is recorded once.
func TestSettleKeepsDoReceipt(t *testing.T) {
	testHome(t)
	e := NewEngine(config.Config{}, "test", io.Discard)
	n := 0
	e.SetReceipt(func(r *Result) string { n++; return "(m, $0.01)" })
	res := &Result{Status: "done", Mode: "read", Model: "x/m", CostUSD: 0.01, Receipt: "(m, $0.01)"}
	if res, _ = e.Settle(context.Background(), res, nil, nil); n != 0 || res.Receipt != "(m, $0.01)" {
		t.Errorf("receipt recomputed %d times: %q", n, res.Receipt)
	}
	if res, _ = e.Settle(context.Background(), &Result{Status: "done", Mode: "read", Model: "x/m"}, nil, nil); n != 1 || res.Receipt == "" {
		t.Errorf("a result from wait gets its receipt: %d %q", n, res.Receipt)
	}
}

// A shell request that asks only because of a VAR=value prefix is answered by p0ssess; anything the
// rules would ask about without the prefix still reaches the user.
func TestPlainCommand(t *testing.T) {
	for _, c := range []struct {
		mode string
		cmd  []string
		want bool
	}{
		{"edit", []string{"GOPROXY=off GOFLAGS=-mod=mod go test ./... 2>&1"}, true},
		{"edit", []string{"CGO_ENABLED=0 go build ./...", "echo done"}, true},
		{"edit", []string{"go test ./..."}, false},
		{"edit", []string{"FOO=1 curl https://example.com"}, false},
		{"edit", []string{"FOO=1 sh -c 'go test'"}, false},
		{"read", []string{"GOFLAGS=-mod=mod go test ./..."}, false},
		{"read", []string{"LC_ALL=C grep -r foo ."}, true},
		{"edit", nil, false},
	} {
		if _, got := plainCommand(c.mode, false, c.cmd); got != c.want {
			t.Errorf("%s %q: %v", c.mode, c.cmd, got)
		}
	}
	if !wildMatch("go test*", "go test ./...") || wildMatch("go test*", "go vet") || !wildMatch("git -C * status", "git -C /x status") {
		t.Error("wildMatch")
	}
}
