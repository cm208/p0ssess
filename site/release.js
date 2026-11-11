// Where p0ssess is published. This is the one place the page reads addresses from.
// While `repo` or `version` is empty, every download control stays disabled
// and no outside link is shown. See README.md, "Going live".
window.P0SSESS = {
  // The repository's page on the git server, without a trailing slash:
  // "https://<host>/<owner>/p0ssess". Release files are read from
  // <repo>/releases/download/v<version>/<file>, which is where Gitea serves them.
  repo: "https://git.christianmoo.re/cmoore/p0ssess",
  // The published release, without the "v": "<major>.<minor>.<patch>".
  version: "0.5.5",
  // The documentation site, or "" to keep the Docs link hidden.
  docs: "https://docs.p0ssess.me",
  // Optional: the lines of that release's SHA256SUMS, to print each sum under its file.
  // Leave a value empty to show only the link to SHA256SUMS.
  sha256: {
    "p0ssess-linux-amd64": "",
    "p0ssess-linux-arm64": "",
    "p0ssess-darwin-arm64": "",
  },
};
