// Where p0ssess is published. This is the one place the pages read addresses from.
// While `repo` or `version` is empty, every download control stays disabled
// and no outside link is shown. See README.md, "Going live".
window.P0SSESS = {
  // The repository's page on the git server, without a trailing slash:
  // "https://<host>/<owner>/p0ssess". Release files are read from
  // <repo>/releases/download/v<version>/<file>, which is where GitHub and Gitea serve them.
  repo: "https://github.com/cm208/p0ssess",
  // The published release, without the "v": "<major>.<minor>.<patch>".
  version: "1.1.1",
  // The documentation site, or "" to keep the Docs link hidden.
  docs: "https://docs.p0ssess.me",
  // Optional: the lines of that release's SHA256SUMS, to print each sum under its file.
  // Leave a value empty to show only the link to SHA256SUMS.
  sha256: {
    "p0ssess-linux-amd64": "1d591e468cd4c9b868b59f98a4fc12ab2502481e4e3e45f2afa04ef08f347da2",
    "p0ssess-linux-arm64": "981099a50c36162ba66b39b7b22aee19f10d8b89480459599ede34bc98c2448c",
    "p0ssess-darwin-arm64": "5763acaceffae3917f2d5d34b05d4c2ff3d54eb84ed898f3d634261ac9eb639e",
    "p0ssess-windows-amd64.exe": "49b2801d1a741a2ed38a2d5ad5e2e17956830a624ffcc17defb82febd6ed1301",
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
