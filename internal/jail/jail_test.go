package jail

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
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

func TestUnitFileMatches(t *testing.T) {
	b, err := os.ReadFile("../../packaging/systemd/p0ssess-jail@.service")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != SystemdUnit() {
		t.Error("packaging/systemd/p0ssess-jail@.service differs from SystemdUnit(); regenerate it with `p0ssess jail unit`")
	}
}

func TestNameFor(t *testing.T) {
	a, b := NameFor("/src/a"), NameFor("/src/b")
	if a == b || !strings.HasPrefix(a, "p0ssess-") || len(a) != len("p0ssess-")+10 {
		t.Errorf("%s %s", a, b)
	}
}
