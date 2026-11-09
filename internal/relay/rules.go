package relay

import "p0ssess/internal/opencode"

// Session rules are appended after OpenCode's allow-all agent defaults and the last match wins, so the
// list starts with a catch-all "ask" and narrows from there. The global policies in the jail config
// still deny the dangerous commands and paths whatever these rules say.

var readShell = []string{
	"cd *", "pwd", "ls", "ls *", "cat *", "head *", "tail *", "wc *", "file *", "stat *", "tree", "tree *",
	"rg *", "grep *", "find *", "sort *", "uniq *", "cut *", "diff *", "echo *", "which *", "basename *", "dirname *",
	"git status", "git status *", "git diff", "git diff *", "git log", "git log *", "git show", "git show *",
	"git --no-pager *", "git grep *", "git ls-files", "git ls-files *", "git blame *", "git rev-parse *",
	"git branch", "git branch --list*", "git branch -a", "git branch -v*",
}

var editShell = []string{
	"python3 *", "python *", "pytest", "pytest *", "go test*", "go build*", "go vet*", "gofmt *",
	"node *", "npm test*", "npm run *", "npx vitest*", "cargo test*", "cargo build*", "cargo check*",
	"make", "make *", "mkdir *", "touch *", "cp *", "mv *", "rm *", "sed *", "awk *", "tr *", "xargs *",
	"git add *", "git restore *", "git checkout -- *", "git stash*", "patch *", "chmod +x *",
}

func rule(action, resource, effect string) opencode.Rule {
	return opencode.Rule{Action: action, Resource: resource, Effect: effect}
}

// Rules returns the per-session ruleset for a mode. "unsafe-allow-all" exists only for the policy tests.
func Rules(mode string) []opencode.Rule {
	if mode == "unsafe-allow-all" {
		return []opencode.Rule{rule("*", "*", "allow")}
	}
	r := []opencode.Rule{
		rule("*", "*", "ask"),
		rule("read", "*", "allow"),
		rule("glob", "*", "allow"),
		rule("grep", "*", "allow"),
		rule("todowrite", "*", "allow"),
		rule("question", "*", "deny"),
		rule("subagent", "*", "deny"),
		rule("skill", "*", "deny"),
		rule("experimental_image_generate", "*", "deny"),
		rule("external_directory", "*", "deny"),
		rule("webfetch", "*", "ask"),
		rule("websearch", "*", "ask"),
		rule("shell", "*", "ask"),
	}
	for _, c := range readShell {
		r = append(r, rule("shell", c, "allow"))
	}
	switch mode {
	case "edit":
		r = append(r, rule("edit", "*", "allow"))
		for _, c := range editShell {
			r = append(r, rule("shell", c, "allow"))
		}
	default:
		r = append(r, rule("edit", "*", "deny"))
	}
	return r
}
