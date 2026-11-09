package jail

import (
	"fmt"
	"net"
	"strings"
)

const NftTable = "p0ssess_jail"

// Blocked ranges: RFC 1918, CGNAT (Tailscale), link-local (pasta's host mapping lives in 169.254/16),
// "this network", multicast, broadcast, and the IPv6 ULA (incl. Tailscale's fd7a:115c:a1e0::/48),
// link-local and multicast ranges.
var (
	BlockedV4 = []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/4", "255.255.255.255"}
	BlockedV6 = []string{"::1", "fc00::/7", "fd00::/8", "fd7a:115c:a1e0::/48", "fe80::/10", "ff00::/8"}
)

// PastaDNS is pasta's DNS forwarder inside the namespace; it is the only allowed link-local destination.
const PastaDNS = "169.254.1.1"

// NftRules builds the egress filter loaded into the pod's network namespace. Loopback inside the pod
// stays open (the server, the proxy and podman's port forwarding use it). extraV4/extraV6 add the host's
// own on-link prefixes, for example a LAN with global IPv6 addresses.
func NftRules(extraV4, extraV6 []string) string {
	v4 := dedupe(append(append([]string{}, BlockedV4...), extraV4...))
	v6 := dedupe(append(append([]string{}, BlockedV6...), extraV6...))
	var b strings.Builder
	fmt.Fprintf(&b, "table inet %s {\n", NftTable)
	b.WriteString("  chain out {\n    type filter hook output priority 0; policy accept;\n")
	b.WriteString("    oifname \"lo\" accept\n")
	fmt.Fprintf(&b, "    ip daddr %s meta l4proto { tcp, udp } th dport 53 accept\n", PastaDNS)
	fmt.Fprintf(&b, "    ip daddr { %s } counter reject with icmpx admin-prohibited\n", strings.Join(v4, ", "))
	fmt.Fprintf(&b, "    ip6 daddr { %s } counter reject with icmpx admin-prohibited\n", strings.Join(v6, ", "))
	b.WriteString("  }\n}\n")
	return b.String()
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// HostPrefixes returns the host's global unicast prefixes (outside the static lists) so that a LAN using
// public addresses is blocked as well.
func HostPrefixes() (v4, v6 []string) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, nil
	}
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || !ipn.IP.IsGlobalUnicast() || ipn.IP.IsPrivate() {
				continue
			}
			ones, bits := ipn.Mask.Size()
			if ones == bits {
				continue // host route, e.g. a VPN /128 already inside a blocked range
			}
			n := &net.IPNet{IP: ipn.IP.Mask(ipn.Mask), Mask: ipn.Mask}
			if ipn.IP.To4() != nil {
				if !inCIDRs(ipn.IP, BlockedV4) {
					v4 = append(v4, n.String())
				}
			} else if !inCIDRs(ipn.IP, BlockedV6) {
				v6 = append(v6, n.String())
			}
		}
	}
	return v4, v6
}

func inCIDRs(ip net.IP, cidrs []string) bool {
	for _, c := range cidrs {
		if !strings.Contains(c, "/") {
			continue
		}
		_, n, err := net.ParseCIDR(c)
		if err == nil && n.Contains(ip) {
			return true
		}
	}
	return false
}
