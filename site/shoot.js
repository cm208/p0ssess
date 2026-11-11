// Screenshot the site with Playwright and a Chromium-based browser, offline.
// Usage: node site/shoot.js [--browser PATH] [--out DIR] [--only name,name] [--full] [--live] [--og FILE]
// Serves site/ on 127.0.0.1 (browsers refuse web fonts over file://) and aborts every
// other request. Console errors, failed requests, outside requests and horizontal overflow
// are printed.
// --full adds a full-page capture at each size.
// --live also captures the motion: the hero's sweep at fixed moments after it starts (desktop
// and phone), the two answers under the abilities and the hand-off figure part-way through,
// and the island over a later section.
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

srv.listen(0, "127.0.0.1", async () => {
  const origin = `http://127.0.0.1:${srv.address().port}`;
  const b = await chromium.launch({ executablePath: opt("browser", "/opt/brave.com/brave/brave"), headless: true,
    args: ["--no-first-run", "--disable-background-networking", "--proxy-server=127.0.0.1:9"] });
  const open = async (viewport, motion, tag) => {
    const ctx = await b.newContext({ viewport, colorScheme: "dark", reducedMotion: motion, deviceScaleFactor: 1 });
    await ctx.route("**/*", (r) => {
      const u = r.request().url();
      if (u.startsWith(origin + "/")) return r.continue();
      log.push(`${tag}: outside request blocked: ${u}`);
      return r.abort();
    });
    const pg = await ctx.newPage();
    pg.on("console", (m) => { if (["error", "warning"].includes(m.type())) log.push(`${tag}: ${m.type()}: ${m.text()}`); });
    pg.on("pageerror", (e) => log.push(`${tag}: pageerror: ${e}`));
    pg.on("response", (r) => { if (r.status() >= 400) log.push(`${tag}: ${r.status()} ${r.url()}`); });
    await pg.goto(origin + "/index.html");
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
