package proc

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Shell returns the command that runs a shell command line the way Claude Code runs status lines:
// sh on Unix; on Windows, Git Bash when it is installed, else PowerShell.
func Shell(line string) (string, []string) {
	if runtime.GOOS != "windows" {
		return "sh", []string{"-c", line}
	}
	if b := GitBash(); b != "" {
		return b, []string{"-c", line}
	}
	return "powershell.exe", []string{"-NoProfile", "-NonInteractive", "-Command", line}
}

// GitBash finds Git for Windows' bash.exe the way Claude Code does: CLAUDE_CODE_GIT_BASH_PATH, else
// next to git.exe. It never returns WSL's bash.exe. It returns "" when there is none, and on Unix.
func GitBash() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	if p := os.Getenv("CLAUDE_CODE_GIT_BASH_PATH"); p != "" && isFile(p) {
		return p
	}
	var dirs []string
	if g, err := exec.LookPath("git"); err == nil {
		d := filepath.Dir(g) // ...\Git\cmd, or ...\Git\mingw64\bin
		dirs = append(dirs, filepath.Join(d, ".."), filepath.Join(d, "..", ".."))
	}
	for _, env := range []string{"ProgramFiles", "ProgramW6432", "LOCALAPPDATA"} {
		if v := os.Getenv(env); v != "" {
			dirs = append(dirs, filepath.Join(v, "Git"), filepath.Join(v, "Programs", "Git"))
		}
	}
	for _, d := range dirs {
		if p := filepath.Join(d, "bin", "bash.exe"); isFile(p) {
			return filepath.Clean(p)
		}
	}
	return ""
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
