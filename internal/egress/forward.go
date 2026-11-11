package egress

import (
	"errors"
	"net"
	"os"
	"os/exec"

	"p0ssess/internal/proc"
)

// ProxyEnv is what a jailed command needs to use the proxy. Tools that resolve names themselves and
// connect directly get no route at all.
func ProxyEnv() []string {
	u := "http://" + ListenAddr
	return []string{"HTTP_PROXY=" + u, "HTTPS_PROXY=" + u, "http_proxy=" + u, "https_proxy=" + u,
		"ALL_PROXY=" + u, "all_proxy=" + u, "NO_PROXY=", "no_proxy=", "npm_config_proxy=" + u, "npm_config_https_proxy=" + u}
}

// StartForwarder runs inside a jailed command's namespaces before the command. It brings loopback up,
// binds ListenAddr, and hands the bound socket to a background copy of exe (`exe serveArg socket`), so
// the port accepts connections before the command starts. It returns once that copy is running.
func StartForwarder(exe, serveArg, socket string) error {
	if _, err := os.Stat(socket); err != nil {
		return errors.New("the egress proxy socket is missing")
	}
	if err := loopbackUp(); err != nil {
		return err
	}
	l, err := net.Listen("tcp", ListenAddr)
	if err != nil {
		return err
	}
	f, err := l.(*net.TCPListener).File()
	l.Close()
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.Command(exe, serveArg, socket)
	cmd.ExtraFiles = []*os.File{f}
	return proc.StartDetached(cmd)
}

// ServeForwarder is the background copy: it accepts on the inherited listener (fd 3) and relays each
// connection to the proxy socket.
func ServeForwarder(socket string) error {
	l, err := net.FileListener(os.NewFile(3, "listener"))
	if err != nil {
		return err
	}
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		go func() {
			u, err := net.Dial("unix", socket)
			if err != nil {
				c.Close()
				return
			}
			splice(c, u, nil)
		}()
	}
}
