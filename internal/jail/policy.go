package jail

import "encoding/json"

// ProxyAddr is where the key-holding OpenRouter proxy listens inside the pod.
const ProxyAddr = "127.0.0.1:4100"

// ProxyKeyPlaceholder is the only OpenRouter key the OpenCode container ever sees.
const ProxyKeyPlaceholder = "p0ssess-proxy-holds-the-key"

// ServerExePath is where the host p0ssess binary is mounted read-only inside the server container. It is
// configured as OpenCode's shell: OpenCode runs "<ServerExePath> -c <cmd>", which dispatches to the jailed
// shell wrapper (see shell.go) rather than a plain /bin/sh.
const ServerExePath = "/opt/p0ssess"

// loopbackAndPrivateHosts are host prefixes a jailed webfetch must never reach: the OpenCode API and the
// key proxy on loopback, plus every private and link-local range. The jailed shell has no network at all,
// but webfetch runs inside the server process, so it is fenced by policy as well.
var loopbackAndPrivateHosts = []string{
	"127.", "localhost", "[::1]", "169.254.", "10.", "192.168.", "172.", "100.",
}

type policy struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Effect   string `json:"effect"`
}

// HardDenyShell lists shell patterns denied by global policy. Policies run after every session and agent
// rule, so an allow-all session cannot undo them. Pipelines are split before matching, so "curl | sh" is
// caught by the bare shell entries rather than by a pipe pattern.
var HardDenyShell = func() []string {
	var out []string
	for _, c := range []string{
		"sudo", "su", "doas", "pkexec", "runuser",
		"ssh", "scp", "sftp", "rsync", "sshpass",
		"docker", "podman", "buildah", "nerdctl", "kubectl",
		"systemctl", "loginctl", "nsenter", "unshare", "chroot",
		"sh", "zsh", "dash", "fish", "ksh", "eval", "exec",
	} {
		out = append(out, c, c+" *")
	}
	return append(out,
		"bash", "bash -*c*",
		"git push", "git push *", "git * push", "git * push *",
		"rm -rf /", "rm -rf /*", "rm -rf ~*",
	)
}()

// protectedPaths are denied for edit (and, where it matters, read). Inside the session directory OpenCode
// matches relative paths, outside it absolute ones, so each entry is listed both ways.
var protectedEdit = []string{".git", ".git/*", ".ssh", ".ssh/*", ".env", ".env.*", ".envrc"}
var protectedRead = []string{".ssh/*", ".env", ".env.*", ".envrc"}

func Policies() []policy {
	p := []policy{
		{"provider.use", "*", "deny"},
		{"provider.use", "openrouter", "allow"},
		{"mcp.use", "*", "deny"},
		{"tool.use", "external_directory:*", "deny"},
		// The read tool resolves its permission resource lexically, then opens the path following symlinks.
		// In-worktree reads carry a relative resource; a read of any absolute path (always outside the
		// worktree) carries that absolute path, so one deny on "/*" blocks every out-of-tree literal read,
		// /proc/1/environ and /state among them. The explicit entries below document the intent.
		{"tool.use", "read:/*", "deny"},
		{"tool.use", "read:/proc/*", "deny"},
		{"tool.use", "read:/state/*", "deny"},
		{"tool.use", "read:/state", "deny"},
	}
	// Webfetch is the only in-process way to reach the loopback API and the key proxy, so deny it to every
	// loopback, link-local and private host, both schemes. The resource is the URL.
	for _, h := range loopbackAndPrivateHosts {
		p = append(p,
			policy{"tool.use", "webfetch:http://" + h + "*", "deny"},
			policy{"tool.use", "webfetch:https://" + h + "*", "deny"})
	}
	for _, s := range HardDenyShell {
		p = append(p, policy{"tool.use", "shell:" + s, "deny"})
	}
	for _, f := range protectedEdit {
		p = append(p, policy{"tool.use", "edit:" + f, "deny"}, policy{"tool.use", "edit:*/" + f, "deny"})
	}
	for _, f := range protectedRead {
		p = append(p, policy{"tool.use", "read:" + f, "deny"}, policy{"tool.use", "read:*/" + f, "deny"})
	}
	return p
}

// OpenCodeConfig is the global config passed to the jailed server through OPENCODE_CONFIG_CONTENT.
func OpenCodeConfig() ([]byte, error) {
	cfg := map[string]any{
		"share":  "disabled",
		"update": "disable",
		// The shell wrapper scrubs the environment and runs each command under fresh user, network, mount
		// and pid namespaces (shell.go). OpenCode runs "<shell> -c <cmd>"; the wrapper is the host binary.
		"shell": ServerExePath,
		// Formatters and language servers run repository-controlled programs inside the server process on
		// save and on open; neither was asked for, so both are off.
		"formatter": false,
		"lsp":       false,
		"permissions": []policy{
			{"external_directory", "*", "deny"},
			{"subagent", "*", "deny"},
		},
		"policies": Policies(),
		// The title agent would send each first prompt to the provider's "small" model, a model nobody chose.
		"agents": map[string]any{"title": map[string]any{"disabled": true}},
		"providers": map[string]any{
			"openrouter": map[string]any{
				"settings": map[string]any{"baseURL": "http://" + ProxyAddr + "/api/v1", "encoding": "none"},
				"body":     map[string]any{"provider": map[string]any{"data_collection": "deny"}},
			},
		},
	}
	return json.Marshal(cfg)
}
