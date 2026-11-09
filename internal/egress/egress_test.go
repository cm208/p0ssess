package egress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAllowedAddr(t *testing.T) {
	extra, _ := ParsePrefixes([]string{"198.41.0.0/24", "2a01:db9::/32", "93.184.215.14"})
	p := Policy{Extra: extra}
	for s, want := range map[string]bool{
		"1.1.1.1": true, "104.18.0.1": true, "2606:4700::1111": true,
		"10.1.2.3": false, "172.20.0.1": false, "192.168.1.1": false, "100.100.100.100": false,
		"127.0.0.1": false, "169.254.1.2": false, "169.254.1.1": false, "0.0.0.0": false, "255.255.255.255": false,
		"224.0.0.1": false, "198.18.0.1": false, "240.0.0.1": false, "192.0.2.1": false,
		"::1": false, "fd7a:115c:a1e0::53": false, "fd00::1": false, "fc00::1": false, "fe80::1": false,
		"ff02::1": false, "::ffff:10.0.0.1": false, "::ffff:1.1.1.1": true, "64:ff9b::a00:1": false,
		"2002:a00:1::1": false, "2001:0:1::1": false,
		"198.41.0.9": false, "2a01:db9::1": false, "93.184.215.14": false, "93.184.215.15": true,
	} {
		if got := p.AllowedAddr(netip.MustParseAddr(s)); got != want {
			t.Errorf("%s: allowed=%v, want %v", s, got, want)
		}
	}
}

// The jail's nftables list must never block less than the proxy.
func TestBlockedCoversCommonRanges(t *testing.T) {
	p := Policy{}
	for _, s := range []string{"10.0.0.0", "100.64.0.0", "127.0.0.0", "169.254.0.0", "172.16.0.0", "192.168.0.0", "fc00::", "fe80::"} {
		if p.AllowedAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s is allowed", s)
		}
	}
}

func TestDialerRefusesBeforeConnecting(t *testing.T) {
	// A listener on loopback proves the refusal is the policy, not a dead port.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	d := Policy{Ports: []int{l.Addr().(*net.TCPAddr).Port}}.Dialer()
	for _, addr := range []string{l.Addr().String(), "localhost:" + fmt.Sprint(l.Addr().(*net.TCPAddr).Port)} {
		c, err := d.DialContext(context.Background(), "tcp", addr)
		if err == nil {
			c.Close()
			t.Errorf("%s: dial succeeded", addr)
		} else if !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v, want ErrRefused", addr, err)
		}
	}
}

// testDial stands in for the internet: "public.test" reaches a local server, everything else goes
// through the real policy dialer.
func testDial(public string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	pd := Policy{}.Dialer()
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		h, p, _ := net.SplitHostPort(addr)
		if h == "public.test" && (p == "443" || p == "80") {
			return net.Dial("tcp", public)
		}
		return pd.DialContext(ctx, network, addr)
	}
}

func serveUnix(t *testing.T, h http.Handler) string {
	sock := filepath.Join(t.TempDir(), "e.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return sock
}

func connect(t *testing.T, sock, target string) (string, net.Conn, *bufio.Reader) {
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	br := bufio.NewReader(c)
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	return resp.Status, c, br
}

func TestConnect(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	sock := serveUnix(t, Handler(Policy{}, testDial(echo.Addr().String()), nil))

	st, c, br := connect(t, sock, "public.test:443")
	if !strings.HasPrefix(st, "200") {
		t.Fatalf("public: %s", st)
	}
	c.Write([]byte("ping\n"))
	line, _ := br.ReadString('\n')
	c.Close()
	if line != "ping\n" {
		t.Errorf("tunnel echoed %q", line)
	}
	for _, target := range []string{"127.0.0.1:4096", "127.0.0.1:4100", "localhost:443", "10.0.0.1:443",
		"169.254.1.2:443", "[::1]:443", "[fd7a:115c:a1e0::53]:443", "100.100.100.100:443", "public.test:22"} {
		st, c, _ := connect(t, sock, target)
		c.Close()
		if !strings.HasPrefix(st, "403") {
			t.Errorf("CONNECT %s: %s, want 403", target, st)
		}
	}
}

func TestAbsoluteURL(t *testing.T) {
	up := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello %s", r.Host)
	})}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	go up.Serve(l)
	defer up.Close()
	sock := serveUnix(t, Handler(Policy{}, testDial(l.Addr().String()), nil))
	client := &http.Client{Transport: &http.Transport{
		Proxy:       func(*http.Request) (*url.URL, error) { return mustURL("http://proxy"), nil },
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return net.Dial("unix", sock) },
	}, Timeout: 10 * time.Second}
	resp, err := client.Get("http://public.test/x")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "hello public.test" {
		t.Errorf("public: %d %q", resp.StatusCode, b)
	}
	for _, u := range []string{"http://127.0.0.1:4096/api/info", "http://127.0.0.1:4100/api/v1/models", "http://localhost/",
		"http://192.168.1.1/", "http://public.test:8080/"} {
		resp, err := client.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", u, resp.StatusCode)
		}
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}
