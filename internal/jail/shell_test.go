package jail

import (
	"slices"
	"strings"
	"testing"
)

func TestCleanShellEnvDropsSecrets(t *testing.T) {
	in := []string{
		"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "TERM=xterm", "HOME=/state/home",
		"OPENCODE_PASSWORD=hunter2", "OPENCODE_SESSION_ID=ses_1", "OPENROUTER_API_KEY=sk-or-xyz",
		"AWS_SECRET_ACCESS_KEY=abc", "GH_TOKEN=def", "DB_PASSWORD=pw", "MY_CREDENTIALS=zzz",
		"GIT_CONFIG_NOSYSTEM=1",
	}
	out := CleanShellEnv(in)
	joined := strings.Join(out, "\n")
	for _, bad := range []string{"OPENCODE_PASSWORD", "OPENCODE_SESSION_ID", "OPENROUTER_API_KEY",
		"AWS_SECRET_ACCESS_KEY", "GH_TOKEN", "DB_PASSWORD", "MY_CREDENTIALS", "hunter2", "sk-or-xyz"} {
		if strings.Contains(joined, bad) {
			t.Errorf("clean env still carries %q:\n%s", bad, joined)
		}
	}
	if !slices.Contains(out, "PATH=/usr/bin:/bin") || !slices.Contains(out, "LANG=C.UTF-8") ||
		!slices.Contains(out, "TERM=xterm") || !slices.Contains(out, "GIT_CONFIG_NOSYSTEM=1") {
		t.Errorf("clean env dropped a basic variable:\n%s", joined)
	}
	// HOME pointed at the masked /state, so it must be rewritten to the writable /tmp, and TMPDIR set.
	if !slices.Contains(out, "HOME=/tmp") || !slices.Contains(out, "TMPDIR=/tmp") {
		t.Errorf("HOME/TMPDIR not redirected to /tmp:\n%s", joined)
	}
}

func TestIsJailedShellInvocation(t *testing.T) {
	if !IsJailedShellInvocation([]string{"-c", "ls"}) {
		t.Error("`-c cmd` should be recognized as a shell invocation")
	}
	if IsJailedShellInvocation([]string{"jail", "up"}) {
		t.Error("a normal subcommand must not be taken for a shell invocation")
	}
}
