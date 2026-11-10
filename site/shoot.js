// Screenshot the site with Playwright and a Chromium-based browser, offline.
// Usage: node site/shoot.js [--browser PATH] [--out DIR] [--only name,name] [--full] [--live]
// Serves site/ on 127.0.0.1 (browsers refuse web fonts over file://) and aborts every
// other request. Console errors and horizontal overflow are printed.
// --live also captures the animation with motion allowed: the hero's hand-off at a fixed
// moment of its loop (desktop and phone), the engine chart while its bars grow, and each
// scene that hands work off while the island shows it.
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
  ".woff2": "font/woff2", ".png": "image/png", ".txt": "text/plain; charset=utf-8" };
const srv = http.createServer((req, res) => {
  let file = path.join(root, decodeURIComponent(new URL(req.url, "http://x").pathname));
  if (file.endsWith(path.sep)) file += "index.html";
  if (!file.startsWith(root) || !fs.existsSync(file) || fs.statSync(file).isDirectory()) { res.writeHead(404); res.end(); return; }
  res.writeHead(200, { "Content-Type": types[path.extname(file)] || "application/octet-stream" });
  fs.createReadStream(file).pipe(res);
});

const desk = ["top", "possession", "engine", "gauge", "media", "chores", "review", "usage", "stakes", "rules", "ghost", "sandbox", "wont", "data", "try", "end"];
const phone = ["top", "possession", "engine", "gauge", "media-cmp", "chores-cmp", "review-cmp", "usage-cmp", "data", "try"];
// Shots that start at a part of a section rather than the section itself.
const parts = { gauge: "#engine .chart", gates: "#engine .gates" };
// Live shots of scenes that hand work off.
const live = ["media", "chores", "usage", "rules"];
// Where in the hero's loop the live shot is taken: the Haiku is possessed and the other model is at work.
const heroAt = 3600;
const selector = (sid) => (sid.endsWith("-cmp") ? `#${sid.slice(0, -4)} .chart` : parts[sid] || "#" + sid);
const log = [];

srv.listen(0, "127.0.0.1", async () => {
  const origin = `http://127.0.0.1:${srv.address().port}`;
  const b = await chromium.launch({ executablePath: opt("browser", "/opt/brave.com/brave/brave"), headless: true,
    args: ["--no-first-run", "--disable-background-networking", "--proxy-server=127.0.0.1:9"] });
  const open = async (viewport, motion, tag) => {
    const ctx = await b.newContext({ viewport, colorScheme: "dark", reducedMotion: motion, deviceScaleFactor: 1 });
    await ctx.route("**/*", (r) => (r.request().url().startsWith(origin + "/") ? r.continue() : r.abort()));
    const pg = await ctx.newPage();
    pg.on("console", (m) => { if (["error", "warning"].includes(m.type())) log.push(`${tag}: ${m.type()}: ${m.text()}`); });
    pg.on("pageerror", (e) => log.push(`${tag}: pageerror: ${e}`));
    await pg.goto(origin + "/index.html");
    await pg.evaluate(() => document.fonts.ready.then(() => true));
    return { ctx, pg };
  };
  const scrollTo = (pg, sel, block) => pg.evaluate(([s, bl]) => document.querySelector(s).scrollIntoView({ block: bl, behavior: "instant" }), [sel, block]);
  // Freeze the hero's loop at one moment, so the capture does not depend on timing.
  const heroFrame = async (pg, file) => {
    await pg.waitForTimeout(400);
    await pg.evaluate((t) => {
      for (const a of document.getAnimations()) {
        if (a.effect && a.effect.target && a.effect.target.closest(".relay")) { a.pause(); a.currentTime = t; }
      }
    }, heroAt);
    await pg.waitForTimeout(150);
    await pg.screenshot({ path: path.join(out, file) });
  };

  for (const [name, viewport, ids] of [["desk", { width: 1280, height: 860 }, desk], ["phone", { width: 390, height: 844 }, phone]]) {
    // Reduced motion renders each scene's final state at once, so captures are deterministic.
    const { ctx, pg } = await open(viewport, "reduce", name);
    const sw = await pg.evaluate(() => document.documentElement.scrollWidth);
    if (sw > viewport.width) log.push(`${name}: horizontal overflow, scrollWidth ${sw}`);
    for (const [i, sid] of ids.entries()) {
      if (!want(sid)) continue;
      if (sid === "top") await pg.evaluate(() => scrollTo(0, 0));
      else if (sid === "end") await pg.evaluate(() => scrollTo(0, document.documentElement.scrollHeight));
      else await scrollTo(pg, selector(sid), name === "desk" && !["engine", "data", "try"].includes(sid) ? "center" : "start");
      await pg.waitForTimeout(450);
      await pg.screenshot({ path: path.join(out, `${name}-dark-${String(i).padStart(2, "0")}-${sid}.png`) });
    }
    if (flag("full") && name === "phone") await pg.screenshot({ path: path.join(out, "phone-dark-full.png"), fullPage: true });
    await ctx.close();
  }

  if (flag("live")) {
    if (want("hero")) {
      const p = await open({ width: 390, height: 844 }, "no-preference", "live-phone");
      await heroFrame(p.pg, "live-hero-phone.png");
      await p.ctx.close();
    }
    const { ctx, pg } = await open({ width: 1280, height: 860 }, "no-preference", "live");
    if (want("hero")) await heroFrame(pg, "live-hero.png");
    if (want("gauge")) {
      // Before anything else scrolls past it: a chart draws its bars once.
      await scrollTo(pg, "#engine .chart", "center");
      await pg.waitForTimeout(260);
      await pg.screenshot({ path: path.join(out, "live-gauge.png") });
    }
    for (const sid of live) {
      if (!want(sid)) continue;
      await scrollTo(pg, "#" + sid, "center");
      await pg.waitForTimeout(400);
      try {
        // The island turns into the live activity when the scene reaches its hand-off line.
        await pg.waitForSelector(".island:not(.is-live)", { timeout: 5000 });
        await pg.waitForSelector(".island.is-live", { timeout: 45000 });
        await pg.waitForTimeout(900);
      } catch {
        log.push(`live: no hand-off seen in #${sid}`);
      }
      await pg.screenshot({ path: path.join(out, `live-${sid}.png`) });
    }
    await ctx.close();
  }
  await b.close();
  srv.close();
  console.log(log.join("\n") || "no console errors, no overflow");
});
