package jail

import "encoding/json"

// ProxyAddr is where the key-holding OpenRouter proxy listens inside the pod.
const ProxyAddr = "127.0.0.1:4100"

// ProxyKeyPlaceholder is the only OpenRouter key the OpenCode container ever sees.
const ProxyKeyPlaceholder = "p0ssess-proxy-holds-the-key"

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
