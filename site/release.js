// Where p0ssess is published. This is the one place the pages read addresses from.
// While `repo` or `version` is empty, every download control stays disabled
// and no outside link is shown. See README.md, "Going live".
window.P0SSESS = {
  // The repository's page on the git server, without a trailing slash:
  // "https://<host>/<owner>/p0ssess". Release files are read from
  // <repo>/releases/download/v<version>/<file>, which is where GitHub and Gitea serve them.
  repo: "https://github.com/cm208/p0ssess",
  // The published release, without the "v": "<major>.<minor>.<patch>".
  version: "1.0.2",
  // The documentation site, or "" to keep the Docs link hidden.
  docs: "https://docs.p0ssess.me",
  // Optional: the lines of that release's SHA256SUMS, to print each sum under its file.
  // Leave a value empty to show only the link to SHA256SUMS.
  sha256: {
    "p0ssess-linux-amd64": "97fc6fc922cfa0b9ec15cbed6bd3b1c63b7f2e09affadf6f644fde059c6f3b32",
    "p0ssess-linux-arm64": "0ba507a22c7d03725f4535af4b280df4ab741c3a6ae3309b72308075ab0beb60",
    "p0ssess-darwin-arm64": "347028bd4e9e73148a4745c1b5297051ae2be6778888a84a3a2fbceef4202096",
    "p0ssess-windows-amd64.exe": "92951cd5a27f717deb2358ec4935dc24a3ed9818b8692ef6a1a005b4a7b13cbb",
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
  },
};
