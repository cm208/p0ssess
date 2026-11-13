// Where p0ssess is published. This is the one place the pages read addresses from.
// While `repo` or `version` is empty, every download control stays disabled
// and no outside link is shown. See README.md, "Going live".
window.P0SSESS = {
  // The repository's page on the git server, without a trailing slash:
  // "https://<host>/<owner>/p0ssess". Release files are read from
  // <repo>/releases/download/v<version>/<file>, which is where GitHub and Gitea serve them.
  repo: "https://github.com/cm208/p0ssess",
  // The published release, without the "v": "<major>.<minor>.<patch>".
  version: "1.0.0",
  // The documentation site, or "" to keep the Docs link hidden.
  docs: "https://docs.p0ssess.me",
  // Optional: the lines of that release's SHA256SUMS, to print each sum under its file.
  // Leave a value empty to show only the link to SHA256SUMS.
  sha256: {
    "p0ssess-linux-amd64": "1488f581931d82157ee0d48dd63fe5f1a595c941eed489ba1f1318f826ac8ebb",
    "p0ssess-linux-arm64": "aac8c489f1e894c7cade7bfab69da380010b8a1ef0dacedb284ecb0e00ee7445",
    "p0ssess-darwin-arm64": "1367ec0fb2597cfc7444c234f6cf971a9b02179361f0f494ef91e470ec658c18",
    "p0ssess-windows-amd64.exe": "8dfe477f6824641eabe2ae088de794d56d2fc0cffbf842b5d46c018a468f01cc",
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
