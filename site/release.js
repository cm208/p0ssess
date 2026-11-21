// Where p0ssess is published. This is the one place the pages read addresses from.
// While `repo` or `version` is empty, every download control stays disabled
// and no outside link is shown. See README.md, "Going live".
window.P0SSESS = {
  // The repository's page on the git server, without a trailing slash:
  // "https://<host>/<owner>/p0ssess". Release files are read from
  // <repo>/releases/download/v<version>/<file>, which is where GitHub and Gitea serve them.
  repo: "https://github.com/cm208/p0ssess",
  // The published release, without the "v": "<major>.<minor>.<patch>".
  version: "1.2.0",
  // The documentation site, or "" to keep the Docs link hidden.
  docs: "https://docs.p0ssess.me",
  // Optional: the lines of that release's SHA256SUMS, to print each sum under its file.
  // Leave a value empty to show only the link to SHA256SUMS.
  sha256: {
    "p0ssess-linux-amd64": "8070c803984254ba7d36853d2b0b23184b1d639333d5608fe270a5edd12a7932",
    "p0ssess-linux-arm64": "3acdce943acdaca62b4597f1480e132eab4b392c6eff5d1690fccded1f594bac",
    "p0ssess-darwin-arm64": "1f41d1343cb661f15d0985f5c2964de98db5bebcd43d7061576f783213517d21",
    "p0ssess-windows-amd64.exe": "cb93d4c52b299dca108d7d50f90c6c8627c88684752c7695c43290a6e05f6732",
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
