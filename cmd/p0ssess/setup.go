package main

// p0ssess setup changes the user's Claude Code settings, and only when asked: /p0ssess-me runs these
// after the user says yes. A plugin cannot ship permission rules or a status line, so this is how the
// relay's tools get allowed (background subagents cannot ask) and how the status line gets wrapped.
// Every change is recorded in ~/.config/p0ssess/setup.json and backed up first; `setup undo` reverts it.

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"p0ssess/internal/config"
	"p0ssess/internal/jail"
	"p0ssess/internal/pme/local"
	"p0ssess/internal/pme/paths"
	"p0ssess/internal/proc"
	"p0ssess/internal/server"
)

const toolPrefix = "mcp__plugin_p0ssess_p0ssess__"

// AllowTools are the tools a background relay or shortcut agent must be able to call without a prompt,
// the engine's read-only tools and history_record_outcome, which only appends to the local ledger. Rule,
// approval and settings changes and shortcut files keep asking: those prompts are the user's approval.
var AllowTools = []string{"run", "wait", "answer_permission", "abort", "models", "direct",
	"recommend", "budget_status", "rules_list", "approvals_list", "tendencies", "history_record_outcome", "shortcuts_suggest"}

// gateTools change the user's limits; their permission prompt is the user's approval, so they are never allowed.
var gateTools = []string{"rules_add", "rules_remove", "approvals_add", "settings_set", "shortcuts_create", "shortcuts_remove"}

const summariesEnv = "CLAUDE_CODE_DISABLE_AGENT_PROGRESS_SUMMARIES"

type setupState struct {
	Settings   string      `json:"settings"`
	AddedAllow []string    `json:"added_allow,omitempty"`
	StatusLine *savedValue `json:"statusline,omitempty"`
	Summaries  *savedValue `json:"summaries,omitempty"`
	Backups    []string    `json:"backups,omitempty"`
}

// savedValue is what a key held before p0ssess changed it; Present false means it did not exist.
type savedValue struct {
	Present bool            `json:"present"`
	Value   json.RawMessage `json:"value,omitempty"`
}

func settingsPath() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "settings.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

func statePath() string { return config.Path("setup.json") }

func loadState() setupState {
	var st setupState
	if b, err := os.ReadFile(statePath()); err == nil {
		json.Unmarshal(b, &st)
	}
	if st.Settings == "" {
		st.Settings = settingsPath()
	}
	return st
}

func (st setupState) save() error {
	b, _ := json.MarshalIndent(st, "", "  ")
	return paths.WriteFileAtomic(statePath(), append(b, '\n'), 0o600)
}

func setupCmd(args []string) error {
	if len(args) == 0 {
		args = []string{"show"}
	}
	fs := flag.NewFlagSet("setup "+args[0], flag.ExitOnError)
	dry := fs.Bool("dry-run", false, "print the change without making it")
	fs.Parse(args[1:])
	st := loadState()
	switch args[0] {
	case "show":
		return setupShow(st)
	case "allow":
		return st.edit(*dry, "allow the plugin's tools", func(o *object) error {
			added, err := addAllow(o)
			st.AddedAllow = mergeUnique(st.AddedAllow, added)
			return err
		})
	case "statusline":
		return setupStatusLine(st, *dry)
	case "summaries":
		return st.edit(*dry, "turn off background agent progress summaries (undocumented)", func(o *object) error {
			env, _ := o.object("env")
			if st.Summaries == nil {
				v, ok := env.get(summariesEnv)
				st.Summaries = &savedValue{Present: ok, Value: v}
			}
			env.set(summariesEnv, json.RawMessage(`"1"`))
			o.setObject("env", env)
			return nil
		})
	case "undo":
		what := "all"
		if fs.NArg() > 0 {
			what = fs.Arg(0)
		}
		return setupUndo(st, what, *dry)
	}
	return fmt.Errorf("setup: unknown command %q (show, allow, statusline, summaries, undo)", args[0])
}

func setupShow(st setupState) error {
	o, err := readObject(st.Settings)
	if err != nil {
		return err
	}
	perms, _ := o.object("permissions")
	var allow []string
	if v, ok := perms.get("allow"); ok {
		json.Unmarshal(v, &allow)
	}
	var missing []string
	for _, t := range AllowTools {
		if !contains(allow, toolPrefix+t) && !contains(allow, strings.TrimSuffix(toolPrefix, "__")) {
			missing = append(missing, toolPrefix+t)
		}
	}
	fmt.Println("settings file:", st.Settings)
	for _, a := range allow {
		if a == strings.TrimSuffix(toolPrefix, "__") || a == toolPrefix+"*" || contains(gateTools, strings.TrimPrefix(a, toolPrefix)) {
			fmt.Printf("warning: %q lets Claude change rules, approvals or settings without asking you; "+
				"those prompts are how you approve spending, so remove it\n", a)
		}
	}
	if len(missing) == 0 {
		fmt.Println("allow rules: all plugin tools the relay needs are allowed")
	} else {
		fmt.Printf("allow rules: %d missing; `p0ssess setup allow` adds:\n", len(missing))
		for _, m := range missing {
			fmt.Println("  " + m)
		}
	}
	sl, _ := o.get("statusLine")
	switch {
	case strings.Contains(string(sl), "p0ssess"):
		fmt.Println("status line: wrapped by p0ssess")
	case len(sl) > 0:
		fmt.Println("status line: yours; `p0ssess setup statusline` keeps it and adds the p0ssess segment after it")
	default:
		fmt.Println("status line: none; `p0ssess setup statusline` adds the p0ssess segment")
	}
	env, _ := o.object("env")
	if v, ok := env.get(summariesEnv); ok {
		fmt.Printf("progress summaries: %s=%s\n", summariesEnv, v)
	}
	if err := server.AgentAvailable(); err != nil {
		fmt.Println("agent path: unavailable,", err, "(the direct tool and the engine still work)")
	} else if exec.Command("podman", "image", "exists", jail.DefaultImage).Run() != nil {
		fmt.Println("agent path: the sandbox image is not built; `p0ssess jail build` downloads OpenCode and builds it (about 1.1 GB)")
	} else {
		fmt.Println("agent path: ready")
	}
	if cfg, err := config.Load(); err == nil {
		if cfg.OpenRouterKeyFile != "" {
			if _, err := os.Stat(cfg.OpenRouterKeyFile); err != nil {
				fmt.Println("OpenRouter key: missing; put it in", cfg.OpenRouterKeyFile, "(mode 0600)")
			} else {
				fmt.Println("OpenRouter key: found")
			}
		}
	}
	if len(st.AddedAllow) > 0 || st.StatusLine != nil || st.Summaries != nil {
		fmt.Println("`p0ssess setup undo` reverts what p0ssess changed")
	}
	return nil
}

func addAllow(o *object) ([]string, error) {
	perms, err := o.object("permissions")
	if err != nil {
		return nil, err
	}
	var allow []string
	if v, ok := perms.get("allow"); ok {
		if err := json.Unmarshal(v, &allow); err != nil {
			return nil, errors.New("permissions.allow is not a list of strings")
		}
	}
	var added []string
	for _, t := range AllowTools {
		if name := toolPrefix + t; !contains(allow, name) {
			allow = append(allow, name)
			added = append(added, name)
		}
	}
	b, _ := json.Marshal(allow)
	perms.set("allow", b)
	o.setObject("permissions", perms)
	return added, nil
}

func setupStatusLine(st setupState, dry bool) error {
	bin, err := installStableCopy(dry)
	if err != nil {
		return err
	}
	return st.edit(dry, "wrap the status line", func(o *object) error {
		cur, ok := o.get("statusLine")
		if strings.Contains(string(cur), "p0ssess") {
			return nil
		}
		if st.StatusLine == nil {
			st.StatusLine = &savedValue{Present: ok, Value: cur}
		}
		var prev struct {
			Command string `json:"command"`
			Padding *int   `json:"padding,omitempty"`
		}
		json.Unmarshal(cur, &prev)
		if !dry {
			e, err := local.Load(paths.Default())
			if err != nil {
				return err
			}
			if _, err := e.SetSetting("statusline.command", prev.Command); err != nil {
				return err
			}
		}
		next := map[string]any{"type": "command", "command": statusCommand(bin)}
		if prev.Padding != nil {
			next["padding"] = *prev.Padding
		}
		b, _ := json.Marshal(next)
		o.set("statusLine", b)
		return nil
	})
}

func setupUndo(st setupState, what string, dry bool) error {
	if len(st.AddedAllow) == 0 && st.StatusLine == nil && st.Summaries == nil {
		fmt.Println("nothing to undo")
		return nil
	}
	return st.edit(dry, "undo "+what, func(o *object) error {
		if (what == "all" || what == "allow") && len(st.AddedAllow) > 0 {
			perms, _ := o.object("permissions")
			var allow, keep []string
			if v, ok := perms.get("allow"); ok {
				json.Unmarshal(v, &allow)
			}
			for _, a := range allow {
				if !contains(st.AddedAllow, a) {
					keep = append(keep, a)
				}
			}
			if keep == nil {
				keep = []string{}
			}
			b, _ := json.Marshal(keep)
			perms.set("allow", b)
			o.setObject("permissions", perms)
			st.AddedAllow = nil
		}
		if (what == "all" || what == "statusline") && st.StatusLine != nil {
			restore(o, "statusLine", st.StatusLine)
			if !dry {
				if e, err := local.Load(paths.Default()); err == nil {
					e.SetSetting("statusline.command", "")
				}
			}
			st.StatusLine = nil
		}
		if (what == "all" || what == "summaries") && st.Summaries != nil {
			env, _ := o.object("env")
			restore(env, summariesEnv, st.Summaries)
			if len(env.keys) == 0 {
				o.del("env")
			} else {
				o.setObject("env", env)
			}
			st.Summaries = nil
		}
		return nil
	})
}

func restore(o *object, key string, v *savedValue) {
	if v.Present {
		o.set(key, v.Value)
	} else {
		o.del(key)
	}
}

// edit reads the settings file, applies change, backs the file up and writes it, then saves the state.
func (st *setupState) edit(dry bool, what string, change func(o *object) error) error {
	o, err := readObject(st.Settings)
	if err != nil {
		return err
	}
	before := o.encode()
	if err := change(o); err != nil {
		return err
	}
	after := o.encode()
	if bytes.Equal(before, after) {
		fmt.Println("no change needed:", what)
		return nil
	}
	if dry {
		fmt.Printf("would %s in %s; the file would become:\n%s", what, st.Settings, after)
		return nil
	}
	if orig, err := os.ReadFile(st.Settings); err == nil {
		bk := config.Path("backups", "settings-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".json")
		if err := paths.WriteFileAtomic(bk, orig, 0o600); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
		st.Backups = append(st.Backups, bk)
	}
	if err := paths.WriteFileAtomic(st.Settings, after, 0o644); err != nil {
		return err
	}
	fmt.Printf("done: %s in %s\n", what, st.Settings)
	return st.save()
}

// installStableCopy copies this binary to ~/.config/p0ssess/bin/p0ssess, which the status line points
// at: the plugin's own path changes with every update.
func installStableCopy(dry bool) (string, error) {
	dst := stablePath()
	if dry {
		return dst, nil
	}
	return dst, copySelf(dst)
}

// stablePath is where the status line's copy of the binary lives.
func stablePath() string {
	if runtime.GOOS == "windows" {
		return config.Path("bin", "p0ssess.exe")
	}
	return config.Path("bin", "p0ssess")
}

// statusCommand is the status line command that runs bin. Claude Code runs it with sh on Unix and, on
// Windows, with Git Bash when it is installed, else PowerShell; both take forward slashes.
func statusCommand(bin string) string {
	if runtime.GOOS != "windows" {
		return fmt.Sprintf("%q statusline", bin)
	}
	b := filepath.ToSlash(bin)
	if proc.GitBash() != "" {
		return `"` + b + `" statusline`
	}
	return "& '" + strings.ReplaceAll(b, "'", "''") + "' statusline"
}

func copySelf(dst string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	if exe == dst {
		return nil
	}
	in, err := os.Open(exe)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".p0ssess-*")
	if err != nil {
		return err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	os.Chmod(tmp.Name(), 0o755)
	// Rename over the old file: a running jail proxy keeps the old one open. Windows cannot replace a
	// running program, but it can move one aside, so a copy the status line is running moves to .old.
	if err := paths.Rename(tmp.Name(), dst); err != nil {
		if runtime.GOOS == "windows" {
			os.Remove(dst + ".old")
			if os.Rename(dst, dst+".old") == nil {
				err = os.Rename(tmp.Name(), dst)
			}
		}
		if err != nil {
			os.Remove(tmp.Name())
			return err
		}
	}
	return os.WriteFile(dst+".version", []byte(version+"\n"), 0o600)
}

// refreshStableCopy updates the status line's copy after a plugin update.
func refreshStableCopy() {
	dst := stablePath()
	if _, err := os.Stat(dst); err != nil {
		return
	}
	if b, _ := os.ReadFile(dst + ".version"); strings.TrimSpace(string(b)) == version {
		return
	}
	copySelf(dst)
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func mergeUnique(a, b []string) []string {
	for _, x := range b {
		if !contains(a, x) {
			a = append(a, x)
		}
	}
	return a
}

// object is a JSON object that keeps its key order, so the user's settings file only changes where
// p0ssess changes it.
type object struct {
	keys []string
	vals map[string]json.RawMessage
}

func parseObject(b []byte) (*object, error) {
	o := &object{vals: map[string]json.RawMessage{}}
	if len(bytes.TrimSpace(b)) == 0 {
		return o, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	t, err := dec.Token()
	if err != nil || t != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		k := kt.(string)
		if _, dup := o.vals[k]; !dup {
			o.keys = append(o.keys, k)
		}
		o.vals[k] = v
	}
	return o, nil
}

func readObject(path string) (*object, error) {
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	o, err := parseObject(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return o, nil
}

func (o *object) get(k string) (json.RawMessage, bool) {
	v, ok := o.vals[k]
	return v, ok
}

func (o *object) set(k string, v json.RawMessage) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *object) del(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, x := range o.keys {
		if x == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

func (o *object) object(k string) (*object, error) {
	v, ok := o.vals[k]
	if !ok {
		return &object{vals: map[string]json.RawMessage{}}, nil
	}
	sub, err := parseObject(v)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", k, err)
	}
	return sub, nil
}

func (o *object) setObject(k string, sub *object) { o.set(k, sub.compact()) }

func (o *object) compact() json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(o.vals[k])
	}
	b.WriteByte('}')
	return b.Bytes()
}

func (o *object) encode() []byte {
	var out bytes.Buffer
	json.Indent(&out, o.compact(), "", "  ")
	out.WriteByte('\n')
	return out.Bytes()
}
