package jail

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// probeScript tries each target from inside a container. "host:port" is a TCP connect, "https://…" a GET.
const probeScript = `
import json, socket, ssl, sys, urllib.request
out = []
for t in sys.argv[1:]:
    r = {"target": t}
    try:
        if t.startswith("https://"):
            with urllib.request.urlopen(t, timeout=8) as resp:
                r["ok"], r["detail"] = True, "HTTP %d" % resp.status
        else:
            host, port = t.rsplit(":", 1)
            host = host.strip("[]")
            fam = socket.AF_INET6 if ":" in host else socket.AF_INET
            s = socket.socket(fam, socket.SOCK_STREAM)
            s.settimeout(4)
            s.connect((host, int(port)))
            s.close()
            r["ok"], r["detail"] = True, "connected"
    except Exception as e:
        r["ok"], r["detail"] = False, type(e).__name__ + ": " + str(e)[:120]
    out.append(r)
print(json.dumps(out))
`

type ProbeResult struct {
	Target string `json:"target"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Probe runs the targets from inside the jailed server container.
func (j *Jail) Probe(ctx context.Context, targets []string) ([]ProbeResult, error) {
	args := append([]string{"exec", j.Name + "-server", "python3", "-c", probeScript}, targets...)
	return runProbe(ctx, args)
}

// ProbeControl runs the same targets from an unfiltered container with the same image, to show that the
// targets are reachable without the jail.
func ProbeControl(ctx context.Context, image string, targets []string) ([]ProbeResult, error) {
	args := append([]string{"run", "--rm", "--network", "pasta", "--cap-drop", "ALL", image, "python3", "-c", probeScript}, targets...)
	return runProbe(ctx, args)
}

func runProbe(ctx context.Context, args []string) ([]ProbeResult, error) {
	out, err := podman(ctx, nil, nil, args...)
	if err != nil {
		return nil, err
	}
	var res []ProbeResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		return nil, fmt.Errorf("probe output: %v", err)
	}
	return res, nil
}

// Identity reports the server container's uid, effective capabilities and /home listing.
func (j *Jail) Identity(ctx context.Context) (string, error) {
	return podman(ctx, nil, nil, "exec", j.Name+"-server", "python3", "-c",
		`import os;print("uid=%d gid=%d" % (os.getuid(), os.getgid()));`+
			`print([l.strip() for l in open("/proc/self/status") if l.startswith("CapEff")][0]);`+
			`print("home:", sorted(os.listdir("/home")));`+
			`print("key in env:", "OPENROUTER_API_KEY" in os.environ and os.environ["OPENROUTER_API_KEY"].startswith("sk-"))`)
}
