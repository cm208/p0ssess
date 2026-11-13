package calibrate

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed samples
var builtinFS embed.FS

// MaxBuiltinBytes caps the built-in samples, which ship inside the binary.
const MaxBuiltinBytes = 3 << 20

// Builtin returns the built-in samples. Their Path is inside the embedded folder; Materialize copies
// the files to disk before a call that sends them.
func Builtin() ([]Sample, error) { return ParseManifest(builtinFS, "samples", true) }

// Materialize writes the built-in sample files under dir (mode 0600) and points each sample's Path at
// its copy on disk. User samples are left as they are.
func Materialize(samples []Sample, dir string) error {
	for i := range samples {
		s := &samples[i]
		if !s.Builtin || s.Path == "" || filepath.IsAbs(s.Path) {
			continue
		}
		b, err := fs.ReadFile(builtinFS, s.Path)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(s.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(dst, b, 0o600); err != nil {
			return err
		}
		s.Path = dst
	}
	return nil
}

// LoadUser reads the user's own samples from dir/answers.toml; a folder without one has none.
func LoadUser(dir string) ([]Sample, error) {
	if _, err := os.Stat(filepath.Join(dir, ManifestName)); os.IsNotExist(err) {
		return nil, nil
	}
	ss, err := ParseManifest(os.DirFS(dir), ".", false)
	if err != nil {
		return nil, err
	}
	for i := range ss {
		if ss[i].Path != "" {
			ss[i].Path = filepath.Join(dir, filepath.FromSlash(ss[i].Path))
		}
	}
	return ss, nil
}
