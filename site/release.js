// Where p0ssess is published. This is the one place the pages read addresses from.
// While `repo` or `version` is empty, every download control stays disabled
// and no outside link is shown. See README.md, "Going live".
window.P0SSESS = {
  // The repository's page on the git server, without a trailing slash:
  // "https://<host>/<owner>/p0ssess". Release files are read from
  // <repo>/releases/download/v<version>/<file>, which is where GitHub and Gitea serve them.
  repo: "https://github.com/cm208/p0ssess",
  // The published release, without the "v": "<major>.<minor>.<patch>".
  version: "1.0.3",
  // The documentation site, or "" to keep the Docs link hidden.
  docs: "https://docs.p0ssess.me",
  // Optional: the lines of that release's SHA256SUMS, to print each sum under its file.
  // Leave a value empty to show only the link to SHA256SUMS.
  sha256: {
    "p0ssess-linux-amd64": "18cb56a8357ffce71857d693b327e19680c20f073181f52b1859560de158e642",
    "p0ssess-linux-arm64": "d9421ef0d310a9d75dcd2c9b0929424bcd14d62370d2d9e6b83c24b8ca2a620f",
    "p0ssess-darwin-arm64": "ecdd484a587330a38f1842e5e31a1c729e090250d26bd9667f866192f2cc12d1",
    "p0ssess-windows-amd64.exe": "3de6d78bfe0601f1bc79a43102f4c367ae9154b00d9f2e8bdc6464e64caddc7b",
  },
  // What is for sale, read by the pricing page (pages.js). See README.md, "Pricing, business and policy pages".
  offer: {
    // The cloud API base, without a trailing slash: "https://cloud.p0ssess.me". "" until the server is
    // hosted; while empty the buy buttons read "Opening soon" and the sign-up form stays off.
    // The pricing page's Content-Security-Policy names this address in connect-src: change both together.
    api: "https://cloud.p0ssess.me",
    // Prices in US cents, shown only while the API cannot be reached. null = not decided yet: the page
    // shows a marked placeholder, never a number.
    prices: { pro_month: 400, pro_year: 3600, team_seat_month: 800 },
    // true shows Polar's checkout in a frame over the pricing page; false sends the buyer to Polar's
    // own page instead. Turn it on only once p0ssess.me is on the Polar organization's list of embed
    // hosts and the privacy policy says the page loads Polar's checkout (README.md, "Checkout").
    embed: false,
    // Polar's customer portal for the organization that sells Pro, or "" to show no link to it.
    portal: "https://polar.sh/new-deal-systems-llc/portal",
  },
};
