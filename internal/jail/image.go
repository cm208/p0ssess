package jail

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// The pinned OpenCode release. The checksum is GitHub's asset digest for opencode-linux-x64.tar.gz.
const (
	OpenCodeVersion   = "2.2.5"
	OpenCodeTarball   = "https://github.com/anomalyco/opencode/releases/download/v2.2.5/opencode-linux-x64.tar.gz"
	OpenCodeTarSHA256 = "09f44302c10fd036912f59c81fd1aef5df45dd06f649197f9dfc9bbe2a79614e"
	OpenCodeBinSHA256 = "8d060e1d2fbb9bb44cf09ae6ed1f3f6a7cff070b5227e2f90970132b23a92050"

	BaseImage = "registry.fedoraproject.org/fedora:44@sha256:ead52905ffe2985aed2881294b944bf225c74944f5afbbd4e2b8cb25dc2f649e"
	// DefaultImage carries a recipe revision after the OpenCode version; bump it when the Containerfile changes.
	DefaultImage = "localhost/p0ssess-opencode:" + OpenCodeVersion + "-1"
)

const containerfile = `FROM ` + BaseImage + `
RUN dnf -y install --setopt=install_weak_deps=False git-core python3 ripgrep findutils diffutils which \
      procps-ng tar gzip patch file && dnf clean all && rm -rf /var/cache/dnf /var/log/dnf*
COPY opencode /usr/local/bin/opencode
RUN chmod 0755 /usr/local/bin/opencode && /usr/local/bin/opencode --version
LABEL org.opencontainers.image.title="p0ssess OpenCode jail" p0ssess.opencode.version="` + OpenCodeVersion + `"
`

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func download(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func extractBinary(tarball, dst string) error {
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("no opencode binary in %s", tarball)
		}
		if err != nil {
			return err
		}
		if filepath.Base(h.Name) != "opencode" || h.Typeflag != tar.TypeReg {
			continue
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	}
}

// Build verifies the pinned OpenCode release and builds the jail image. tarball may be "" to download it.
func Build(ctx context.Context, image, tarball string, out io.Writer) error {
	dir, err := os.MkdirTemp("", "p0ssess-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if tarball == "" {
		tarball = filepath.Join(dir, "opencode.tar.gz")
		fmt.Fprintf(out, "downloading %s\n", OpenCodeTarball)
		if err := download(ctx, OpenCodeTarball, tarball); err != nil {
			return err
		}
	}
	sum, err := sha256File(tarball)
	if err != nil {
		return err
	}
	if sum != OpenCodeTarSHA256 {
		return fmt.Errorf("OpenCode tarball checksum mismatch: got %s, want %s", sum, OpenCodeTarSHA256)
	}
	bin := filepath.Join(dir, "opencode")
	if err := extractBinary(tarball, bin); err != nil {
		return err
	}
	if sum, _ := sha256File(bin); sum != OpenCodeBinSHA256 {
		return fmt.Errorf("OpenCode binary checksum mismatch: got %s", sum)
	}
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte(containerfile), 0o644); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "podman", "build", "--pull=missing", "-t", image, "-f", filepath.Join(dir, "Containerfile"), dir)
	cmd.Stdout, cmd.Stderr = out, out
	return cmd.Run()
}
