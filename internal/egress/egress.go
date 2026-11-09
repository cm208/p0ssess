// Package egress is the shell's way out to the public internet when a project opts in. It has two
// halves:
//
//   - Serve runs in its own container in the jail pod (no key, read-only, no capabilities) and listens on
//     a Unix socket. It is an HTTP proxy (CONNECT and absolute-URI requests) that resolves each name itself
//     and connects only to public unicast addresses on the allowed ports. Loopback (the OpenCode API and the
//     key proxy), private, CGNAT (Tailscale), link-local, ULA, multicast, documentation and translation
//     ranges, and the host's own prefixes are refused. The check runs on the address being connected, after
//     resolution, so a name that resolves to a private address is refused too.
//   - Forward runs inside a jailed command's own network namespace, which has nothing but loopback. It
//     brings loopback up and relays 127.0.0.1:3128 to the socket. Unix sockets are files, not network
//     endpoints, so the socket is the namespace's only way out, and only to the proxy.
package egress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ListenAddr is where a jailed command finds the proxy, inside its own network namespace.
const ListenAddr = "127.0.0.1:3128"

// DefaultPorts are the destination ports the proxy connects to.
var DefaultPorts = []int{80, 443}

// Blocked lists every destination range that is not the public internet. It is a superset of the jail's
// nftables deny list.
var Blocked = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
	"192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
	"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "255.255.255.255/32",
	"::/128", "::1/128", "::ffff:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/32",
	"2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10", "ff00::/8",
)

func mustPrefixes(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(s))
	for _, p := range s {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}

// Policy decides which destinations the proxy may connect to.
type Policy struct {
	// Extra are more ranges to refuse: the host's own prefixes and any the user configured.
	Extra []netip.Prefix
	Ports []int
}

// AllowedAddr reports whether ip is a public unicast address outside every blocked range.
func (p Policy) AllowedAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.Zone() != "" {
		return false
	}
	for _, list := range [][]netip.Prefix{Blocked, p.Extra} {
		for _, n := range list {
			if n.Contains(ip) {
				return false
			}
		}
	}
	return true
}

func (p Policy) allowedPort(port int) bool {
	ports := p.Ports
	if len(ports) == 0 {
		ports = DefaultPorts
	}
	for _, x := range ports {
		if x == port {
			return true
		}
	}
	return false
}

// ErrRefused is returned for every destination outside the policy. It says nothing about why, so a
// command cannot learn whether an internal name exists.
var ErrRefused = errors.New("destination refused: only public internet addresses on ports 80 and 443")

// Dialer connects only where the policy allows. The check runs in Control, on the resolved address the
// socket is about to connect to.
func (p Policy) Dialer() *net.Dialer {
	return &net.Dialer{
		Timeout: 15 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !p.AllowedAddr(ap.Addr()) || !p.allowedPort(int(ap.Port())) {
				return ErrRefused
			}
			return nil
		},
	}
}

// Handler is the proxy. dial is normally Policy.Dialer().DialContext; tests replace it.
func Handler(p Policy, dial func(ctx context.Context, network, addr string) (net.Conn, error), logf func(string, ...any)) http.Handler {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	checkPort := func(hostport string) bool {
		_, ps, err := net.SplitHostPort(hostport)
		if err != nil {
			return false
		}
		n, err := strconv.Atoi(ps)
		return err == nil && p.allowedPort(n)
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           dial,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          16,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
	}
	rp := &httputil.ReverseProxy{
		Rewrite:   func(pr *httputil.ProxyRequest) { pr.Out.Header.Del("Proxy-Authorization") },
		Transport: tr,
		ErrorLog:  log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, ErrRefused) {
				http.Error(w, ErrRefused.Error(), http.StatusForbidden)
				return
			}
			http.Error(w, "upstream error", http.StatusBadGateway)
		},
	}
	sem := make(chan struct{}, 64)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		default:
			http.Error(w, "too many connections", http.StatusServiceUnavailable)
			return
		}
		if r.Method == http.MethodConnect {
			target := r.Host
			if !checkPort(target) {
				logf("CONNECT %s refused (port)", target)
				http.Error(w, ErrRefused.Error(), http.StatusForbidden)
				return
			}
			up, err := dial(r.Context(), "tcp", target)
			if err != nil {
				logf("CONNECT %s refused", target)
				if errors.Is(err, ErrRefused) || isDNS(err) {
					http.Error(w, ErrRefused.Error(), http.StatusForbidden)
				} else {
					http.Error(w, "upstream error", http.StatusBadGateway)
				}
				return
			}
			hj, ok := w.(http.Hijacker)
			if !ok {
				up.Close()
				http.Error(w, "hijack unsupported", http.StatusInternalServerError)
				return
			}
			conn, brw, err := hj.Hijack()
			if err != nil {
				up.Close()
				return
			}
			logf("CONNECT %s", target)
			conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
			splice(conn, up, brw.Reader)
			return
		}
		if r.URL.Scheme != "http" || r.URL.Host == "" {
			http.Error(w, "send absolute http:// URLs or CONNECT", http.StatusBadRequest)
			return
		}
		host := r.URL.Host
		if _, _, err := net.SplitHostPort(host); err != nil {
			host = net.JoinHostPort(strings.Trim(host, "[]"), "80")
		}
		if !checkPort(host) {
			logf("%s %s refused (port)", r.Method, host)
			http.Error(w, ErrRefused.Error(), http.StatusForbidden)
			return
		}
		logf("%s %s", r.Method, host)
		rp.ServeHTTP(w, r)
	})
}

func isDNS(err error) bool {
	var de *net.DNSError
	return errors.As(err, &de)
}

// splice copies both ways until either side closes. pending holds bytes the HTTP server already read.
func splice(a net.Conn, b net.Conn, pending *bufio.Reader) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		var src io.Reader = a
		if pending != nil && pending.Buffered() > 0 {
			src = io.MultiReader(io.LimitReader(pending, int64(pending.Buffered())), a)
		}
		io.Copy(b, src)
		closeWrite(b)
	}()
	go func() {
		defer wg.Done()
		io.Copy(a, b)
		closeWrite(a)
	}()
	wg.Wait()
	a.Close()
	b.Close()
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	} else {
		c.Close()
	}
}

// Serve listens on the Unix socket at path until the process exits.
func Serve(path string, p Policy) error {
	os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	// The jailed command connects as a different user namespace's root; the directory is private to the jail.
	if err := os.Chmod(path, 0o666); err != nil {
		return err
	}
	logger := log.New(os.Stderr, "egress ", log.LstdFlags)
	srv := &http.Server{
		Handler:           Handler(p, p.Dialer().DialContext, logger.Printf),
		ReadHeaderTimeout: 30 * time.Second,
	}
	return srv.Serve(l)
}

// ParsePrefixes reads CIDRs (or single addresses) for Policy.Extra.
func ParsePrefixes(list []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !strings.Contains(s, "/") {
			a, err := netip.ParseAddr(s)
			if err != nil {
				return nil, fmt.Errorf("blocked range %q: %v", s, err)
			}
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return nil, fmt.Errorf("blocked range %q: %v", s, err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}
