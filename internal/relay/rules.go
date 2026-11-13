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
	"git branch", "git branch --list*", "git branch -a", "git branch -v*", "git branch --show-current",
	"git worktree list", "git worktree list *",
	// Checks models run on their own to orient themselves; none of them changes anything.
	"id", "id *", "whoami", "true", "false", "test *", "[ *", "printf *", "nl *", "od *", "cmp *", "comm *",
	"sha256sum *", "sha1sum *", "md5sum *", "du *", "df", "df *", "realpath *", "readlink *", "date", "date *",
	"mount", "findmnt", "findmnt *", "column *", "jq *", "python3 --version", "python --version",
}

// editShell is what edit mode runs without asking: writing in the session's worktree and running the
// project's own build and test commands. Edit mode already runs the project's code (python3, node, make),
// so these widen nothing; the jail stays the boundary (no network unless the project opted in, the
// repository itself mounted read-only, the global policies). Network fetches and installs stay in
// netShell, and anything else still asks.
var editShell = []string{
	"python3 *", "python *", "pytest", "pytest *", "go test*", "go build*", "go vet*", "gofmt *", "go run *",
	"node *", "npm test*", "npm run *", "npx vitest*", "npx jest*", "npx mocha*", "npx tsc*", "npx eslint*",
	"yarn test*", "yarn run *", "pnpm test*", "pnpm run *", "bun test*", "deno test*",
	"cargo test*", "cargo build*", "cargo check*", "cargo clippy*", "cargo fmt*", "cargo nextest*",
	"mvn test*", "mvn -q test*", "gradle test*", "./gradlew test*", "dotnet test*", "dotnet build*",
	"bundle exec rspec*", "rspec*", "rake test*", "ruby *", "phpunit*", "vendor/bin/phpunit*", "php *",
	"mix test*", "swift test*", "swift build*", "ctest*", "uv run *", "tox*", "./*",
	"make", "make *", "mkdir *", "touch *", "cp *", "mv *", "rm *", "sed *", "awk *", "tr *", "xargs *",
	"tee *", "ln -s *", "chmod +x *", "patch *",
	"git add *", "git restore *", "git checkout -- *", "git stash*", "git rm *", "git reset *", "git commit *",
	"git -C * status", "git -C * status *", "git -C * diff*", "git -C * log*", "git -C * show*",
	"git -C * rev-parse *", "git -C * ls-files*", "git -C * worktree list*",
}

// netShell are package installs and fetches, allowed in edit mode only when the project has shell
// internet. Without it they would fail anyway, so they keep asking.
var netShell = []string{
	"pip install *", "pip3 install *", "python3 -m pip install *", "npm install*", "npm ci*", "pnpm install*",
	"yarn install*", "go mod download*", "go mod tidy*", "go get *", "cargo fetch*", "curl *", "git fetch*", "git clone *",
}

func rule(action, resource, effect string) opencode.Rule {
	return opencode.Rule{Action: action, Resource: resource, Effect: effect}
}

// Rules returns the per-session ruleset for a mode. "unsafe-allow-all" exists only for the policy tests.
// internet adds the fetch and install commands to edit mode.
func Rules(mode string, internet bool) []opencode.Rule {
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
		if internet {
			for _, c := range netShell {
				r = append(r, rule("shell", c, "allow"))
			}
		}
	default:
		r = append(r, rule("edit", "*", "deny"))
	}
	return r
}
