// Screenshot the site with Playwright and a Chromium-based browser, offline.
// Usage: node site/shoot.js [--browser PATH] [--out DIR] [--only name,name] [--full] [--live] [--pages] [--og FILE]
// Serves site/ on 127.0.0.1 (browsers refuse web fonts over file://) and aborts every
// other request. Console errors, failed requests, outside requests and horizontal overflow
// are printed.
// --full adds a full-page capture at each size.
// --live also captures the motion: the hero's sweep at fixed moments after it starts (desktop
// and phone), the two answers under the abilities and the hand-off figure part-way through,
// and the island over a later section.
// --pages adds full-page captures of the pages beside the home page (pricing, business, data,
// terms, privacy, refunds) at each size, and of the pricing page in each state: as it ships,
// then against a cloud API that this file answers itself (nothing leaves the machine): opening
// soon, a sign-up refused and accepted, and checkout open. --only takes page names too.
// --og writes the share image: the top of the hero at 1200x630, reduced motion, device scale 1.
// Needs playwright-core: `npm i playwright-core`, or set PLAYWRIGHT_CORE to a copy of it
// (Playwright for Python ships one as playwright/driver/package, next to its own node).
const { chromium } = require(process.env.PLAYWRIGHT_CORE || "playwright-core");
const http = require("http");
const fs = require("fs");
const path = require("path");

const argv = process.argv.slice(2);
const opt = (name, dflt) => { const i = argv.indexOf("--" + name); return i < 0 ? dflt : argv[i + 1]; };
const flag = (name) => argv.includes("--" + name);
const root = __dirname;
const out = path.resolve(opt("out", path.join(root, "shots")));
const only = opt("only", "").split(",").filter(Boolean);
const want = (name) => !only.length || only.includes(name);
fs.mkdirSync(out, { recursive: true });

const types = { ".html": "text/html; charset=utf-8", ".css": "text/css", ".js": "text/javascript", ".svg": "image/svg+xml",
  ".woff2": "font/woff2", ".png": "image/png", ".webp": "image/webp", ".txt": "text/plain; charset=utf-8" };
const srv = http.createServer((req, res) => {
  let file = path.join(root, decodeURIComponent(new URL(req.url, "http://x").pathname));
  if (file.endsWith(path.sep)) file += "index.html";
  if (!file.startsWith(root) || !fs.existsSync(file) || fs.statSync(file).isDirectory()) { res.writeHead(404); res.end(); return; }
  res.writeHead(200, { "Content-Type": types[path.extname(file)] || "application/octet-stream" });
  fs.createReadStream(file).pipe(res);
});

// The hero (its film test), then each section.
const ids = ["top", "film", "can", "how", "engine", "more", "limits", "try", "end"];
const sizes = [["desk", { width: 1440, height: 900 }], ["phone", { width: 390, height: 844 }]];
// Moments of the hero's sweep, in ms after the timeline is on screen: Claude alone, part-way, done.
const sweepAt = [700, 2300, 5200];
const log = [];

// The pages beside the home page.
const pages = ["pricing", "business", "data", "terms", "privacy", "refunds"];
// The cloud API, mocked. The address is the one the pricing page's CSP allows. These amounts
// are test values that exist only here: no price has been decided, and none is in the site's files.
const CLOUD = "https://cloud.p0ssess.me";
const TEST = { year: 12345, month: 1234, seat: 567, hours: 7 };
const releaseWithApi = () => {
  const src = fs.readFileSync(path.join(root, "release.js"), "utf8");
  const out = src.replace(/(\n\s*api:\s*)""/, `$1"${CLOUD}"`);
  if (out === src) log.push("release.js: no empty offer.api to replace");
  return out;
};
const plan = (id, tier, interval, amount, placeholder, more) => ({ id, tier, interval, name: id, available: true, placeholder,
  price: { amount_cents: amount, currency: "usd", display: "test" }, ...more });
const offer = (checkout, placeholder) => ({ checkout, start_url: CLOUD + "/pro", plans: [
  plan("pro_yearly", "pro", "year", TEST.year, placeholder, { recommended: true, sandbox_hours_per_month: TEST.hours }),
  plan("pro_monthly", "pro", "month", TEST.month, placeholder, { sandbox_hours_per_month: TEST.hours }),
  plan("team_monthly", "team", "month", TEST.seat, placeholder, { per_seat: true, available: false }),
] });
const cors = { "access-control-allow-origin": "*" };
const cloudApi = (checkout, placeholder) => (req, tag) => {
  const route = req.method() + " " + new URL(req.url()).pathname;
  if (route === "GET /v1/pro/offer") return { status: 200, headers: cors, contentType: "application/json", body: JSON.stringify(offer(checkout, placeholder)) };
  if (route === "POST /v1/waitlist") {
    const form = new URLSearchParams(req.postData() || "");
    const simple = (req.headers()["content-type"] || "").startsWith("application/x-www-form-urlencoded");
    if (!simple) log.push(`${tag}: the sign-up was not sent form-encoded, so a browser would preflight it`);
    const good = simple && /^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(form.get("email") || "") && ["pro", "team"].includes(form.get("plan")) && form.get("source") === "site";
    return good ? { status: 204, headers: cors } : { status: 400, headers: cors, contentType: "application/json", body: JSON.stringify({ error: "bad_request", message: "test" }) };
  }
  log.push(`${tag}: unexpected call to the cloud API: ${route}`);
  return { status: 404, headers: cors };
};

srv.listen(0, "127.0.0.1", async () => {
  const origin = `http://127.0.0.1:${srv.address().port}`;
  const b = await chromium.launch({ executablePath: opt("browser", "/opt/brave.com/brave/brave"), headless: true,
    args: ["--no-first-run", "--disable-background-networking", "--proxy-server=127.0.0.1:9"] });
  // `at` is the page to open. `cloud`, when given, answers the cloud API, and release.js is served
  // with that API's address in place of the empty one, which is how the pricing page turns it on.
  const open = async (viewport, motion, tag, { at = "/index.html", cloud = null } = {}) => {
    const ctx = await b.newContext({ viewport, colorScheme: "dark", reducedMotion: motion, deviceScaleFactor: 1 });
    await ctx.route("**/*", (r) => {
      const u = r.request().url();
      if (cloud && u === origin + "/release.js") return r.fulfill({ contentType: types[".js"], body: releaseWithApi() });
      if (u.startsWith(origin + "/")) return r.continue();
      if (cloud && u.startsWith(CLOUD + "/")) return r.fulfill(cloud(r.request(), tag));
      log.push(`${tag}: outside request blocked: ${u}`);
      return r.abort();
    });
    const pg = await ctx.newPage();
    pg.on("console", (m) => { if (["error", "warning"].includes(m.type())) log.push(`${tag}: ${m.type()}: ${m.text()}`); });
    pg.on("pageerror", (e) => log.push(`${tag}: pageerror: ${e}`));
    pg.on("response", (r) => { if (r.status() >= 400) log.push(`${tag}: ${r.status()} ${r.url()}`); });
    await pg.goto(origin + at);
    await pg.evaluate(() => document.fonts.ready.then(() => true));
    return { ctx, pg };
  };
  const scrollTo = (pg, sel, block) => pg.evaluate(([s, bl]) => document.querySelector(s).scrollIntoView({ block: bl, behavior: "instant" }), [sel, block]);
  const shot = (pg, name) => pg.screenshot({ path: path.join(out, name) });

  for (const [name, viewport] of sizes) {
    // Reduced motion renders every part in its finished state at once, so captures are deterministic.
    const { ctx, pg } = await open(viewport, "reduce", name);
    await pg.waitForTimeout(500);
    const sw = await pg.evaluate(() => document.documentElement.scrollWidth);
    if (sw > viewport.width) log.push(`${name}: horizontal overflow, scrollWidth ${sw}`);
    for (const [i, id] of ids.entries()) {
      if (!want(id)) continue;
      if (id === "top") await pg.evaluate(() => scrollTo(0, 0));
      else if (id === "end") await pg.evaluate(() => scrollTo(0, document.documentElement.scrollHeight));
      else await scrollTo(pg, "#" + id, "start");
      await pg.waitForTimeout(400);
      await shot(pg, `${name}-${String(i).padStart(2, "0")}-${id}.png`);
    }
    if (flag("full")) await pg.screenshot({ path: path.join(out, `${name}-full.png`), fullPage: true });
    await ctx.close();
  }

  if (flag("live")) {
    for (const [name, viewport] of sizes) {
      if (!want("hero")) continue;
      const { ctx, pg } = await open(viewport, "no-preference", "live-" + name);
      // The sweep starts once the timeline is on screen; on a phone that takes a scroll.
      if (name === "phone") await scrollTo(pg, ".lanes", "center");
      const t0 = Date.now();
      for (const [i, at] of sweepAt.entries()) {
        const wait = at - (Date.now() - t0);
        if (wait > 0) await pg.waitForTimeout(wait);
        await shot(pg, `live-hero-${name}-${i + 1}.png`);
      }
      await ctx.close();
    }
    const { ctx, pg } = await open(sizes[0][1], "no-preference", "live");
    if (want("can")) {
      // The two answers under the abilities, part-way through coming back.
      await scrollTo(pg, "#can .grid", "center");
      await pg.waitForTimeout(1300);
      await shot(pg, "live-can.png");
    }
    if (want("how")) {
      await scrollTo(pg, ".relay", "center");
      await pg.waitForTimeout(2200);
      await shot(pg, "live-how.png");
    }
    if (want("engine")) {
      await scrollTo(pg, "#engine", "start");
      await pg.waitForTimeout(700);
      await shot(pg, "live-engine.png");
    }
    await ctx.close();
  }

  if (flag("pages")) {
    for (const [name, viewport] of sizes) {
      const full = (pg, file) => pg.screenshot({ path: path.join(out, `${name}-${file}.png`), fullPage: true });
      const overflow = async (pg, tag) => {
        const sw = await pg.evaluate(() => document.documentElement.scrollWidth);
        if (sw > viewport.width) log.push(`${tag}: horizontal overflow, scrollWidth ${sw}`);
      };
      const check = async (pg, tag, what, test) => { if (!(await pg.evaluate(test))) log.push(`${tag}: check failed: ${what}`); };
      for (const slug of pages) {
        if (!want(slug)) continue;
        const { ctx, pg } = await open(viewport, "reduce", `${name} ${slug}`, { at: `/${slug}/` });
        await pg.waitForTimeout(400);
        await overflow(pg, `${name} ${slug}`);
        await full(pg, slug);
        await ctx.close();
      }
      if (!want("pricing")) continue;
      // As it ships, and with an API that cannot answer: the buttons are links that open an email.
      for (const [label, cloud] of [["as shipped", null], ["API unreachable", () => ({ status: 503, headers: cors })]]) {
        const t = `${name} pricing, ${label}`;
        const o = await open(viewport, "reduce", t, cloud ? { at: "/pricing/", cloud } : { at: "/pricing/" });
        await o.pg.waitForTimeout(600);
        await check(o.pg, t, "Pro and Team are mailto links to support, with no form shown", `(() => {
          const m = (plan, word) => { const a = document.querySelector('[data-buy="' + plan + '"] [data-mail]');
            return !a.hidden && a.href === "mailto:support@p0ssess.me?subject=Notify%20me%20when%20p0ssess%20" + word + "%20opens"; };
          return m("pro", "Pro") && m("team", "Team") && [...document.querySelectorAll("[data-open], form.wl")].every((el) => el.hidden); })()`);
        await overflow(o.pg, t);
        await o.ctx.close();
      }
      const digits = Object.values(TEST).slice(0, 3).map((c) => (c / 100).toFixed(2));

      // Opening soon: the API is there, its prices are placeholders, and the buttons take an address.
      let tag = `${name} pricing, opening soon`;
      let { ctx, pg } = await open(viewport, "reduce", tag, { at: "/pricing/", cloud: cloudApi("opening_soon", true) });
      await pg.waitForSelector('[data-buy="pro"] [data-open]:not([hidden])');
      await pg.waitForTimeout(400);
      await check(pg, tag, "a placeholder price is never shown as a number", `(() => { const t = document.querySelector(".tiers").textContent;
        return ${JSON.stringify(digits)}.every((d) => !t.includes(d)) && [...document.querySelectorAll("[data-price]")].every((el) => el.querySelector(".ph-tag")); })()`);
      await check(pg, tag, "the Pro button reads as agreed", `document.querySelector('[data-buy="pro"] [data-open]').textContent === "Opening soon, get notified"`);
      await overflow(pg, tag);
      await full(pg, "pricing-mock-soon");
      // A sign-up that is refused, then one that is accepted, for Pro and for Team.
      await pg.click('[data-buy="pro"] [data-open]');
      await pg.fill("#wl-pro-email", "not an address");
      await pg.click("#wl-pro .wl-send");
      await pg.waitForFunction(() => document.querySelector("#wl-pro-say").textContent !== "");
      await full(pg, "pricing-mock-waitlist-bad");
      await pg.fill("#wl-pro-email", "reader@example.com");
      await pg.click("#wl-pro .wl-send");
      await pg.waitForSelector('[data-buy="pro"].done');
      await pg.click('[data-buy="team"] [data-open]');
      await pg.fill("#wl-team-email", "reader@example.com");
      await pg.keyboard.press("Enter");
      await pg.waitForSelector('[data-buy="team"].done');
      await pg.evaluate(() => document.activeElement && document.activeElement.blur());
      await overflow(pg, tag);
      await full(pg, "pricing-mock-waitlist-ok");
      await ctx.close();

      // Checkout open, with the test prices. The capture says so across its top.
      tag = `${name} pricing, checkout open`;
      ({ ctx, pg } = await open(viewport, "reduce", tag, { at: "/pricing/", cloud: cloudApi("open", false) }));
      await pg.waitForSelector("[data-start]:not([hidden])");
      await pg.waitForTimeout(400);
      await check(pg, tag, "Pro links to checkout and Team has nothing to buy", `document.querySelector('[data-buy="pro"] [data-start]').href === "${CLOUD}/pro"
        && !document.querySelector('[data-buy="team"] [data-start]') && document.querySelector('[data-buy="pro"] [data-open]').hidden`);
      await check(pg, tag, "prices are formatted from cents", `${JSON.stringify(digits)}.every((d) => document.querySelector(".tiers").textContent.includes(d))`);
      await overflow(pg, tag);
      await pg.evaluate(() => {
        const note = document.createElement("p");
        note.textContent = "Screenshot harness: a mocked offer with test prices. No price has been decided.";
        Object.assign(note.style, { margin: "0", padding: "8px 16px", background: "#ffaf00", color: "#000", font: "13px/1.4 monospace", textAlign: "center" });
        document.body.prepend(note);
      });
      await full(pg, "pricing-mock-open");
      await ctx.close();
    }
  }

  const og = opt("og", "");
  if (og) {
    const { ctx, pg } = await open({ width: 1200, height: 630 }, "reduce", "og");
    await pg.waitForTimeout(500);
    // The share image is the promise and the frame; what sits below them would be cut mid-line.
    await pg.evaluate(() => document.querySelectorAll(".readout, .tracks, .film-note").forEach((el) => { el.style.visibility = "hidden"; }));
    await pg.screenshot({ path: path.resolve(og) });
    await ctx.close();
  }

  await b.close();
  srv.close();
  console.log(log.join("\n") || "no console errors, no failed or outside requests, no overflow");
});
