"""Screenshot the site with Playwright and a Chromium-based browser, offline.
Usage: python -I site/shoot.py [--browser PATH] [--out DIR] [--only name,name] [--full] [--live]
Serves site/ on 127.0.0.1 (browsers refuse web fonts over file://) and aborts every
other request. Console errors and horizontal overflow are printed.
--live also captures the desktop animation mid-flight (motion allowed): each scene with a
hand-off is shot while the island shows it, and each chart while its bars grow."""
import argparse
import functools
import http.server
import threading
from pathlib import Path
from playwright.sync_api import sync_playwright

root = Path(__file__).resolve().parent
ap = argparse.ArgumentParser()
ap.add_argument("--browser", default="/opt/brave.com/brave/brave")
ap.add_argument("--out", default=str(root / "shots"))
ap.add_argument("--only", default="", help="comma-separated shot names to take")
ap.add_argument("--full", action="store_true", help="also save full-page phone captures")
ap.add_argument("--live", action="store_true", help="also capture the desktop animation, with motion allowed")
args = ap.parse_args()
out = Path(args.out)
out.mkdir(parents=True, exist_ok=True)
only = [s for s in args.only.split(",") if s]


class Quiet(http.server.SimpleHTTPRequestHandler):
    def log_message(self, *a):
        pass


handler = functools.partial(Quiet, directory=str(root))
srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
threading.Thread(target=srv.serve_forever, daemon=True).start()
origin = f"http://127.0.0.1:{srv.server_port}"
url = origin + "/index.html"
log = []

desk = ["top", "engine", "gauge", "media", "chores", "review", "usage", "stakes", "rules", "ghost", "sandbox", "wont", "data", "try", "end"]
phone = ["top", "engine", "gauge", "media-cmp", "chores-cmp", "review-cmp", "usage-cmp", "data", "try"]
# Shots that start at a part of a section rather than the section itself.
parts = {"gauge": "#engine .chart", "gates": "#engine .gates"}
# Live shots: scenes that hand work off, and the first chart.
live = ["media", "chores", "usage", "rules"]


def selector(sid):
    if sid.endswith("-cmp"):
        return f"#{sid[:-4]} .chart"
    return parts.get(sid, "#" + sid)


def page(browser, vp, motion, tag):
    ctx = browser.new_context(viewport=vp, color_scheme="dark", reduced_motion=motion, device_scale_factor=1)
    ctx.route("**/*", lambda r: r.continue_() if r.request.url.startswith(origin + "/") else r.abort())
    pg = ctx.new_page()
    pg.on("console", lambda m: log.append(f"{tag}: {m.type}: {m.text}") if m.type in ("error", "warning") else None)
    pg.on("pageerror", lambda e: log.append(f"{tag}: pageerror: {e}"))
    pg.goto(url)
    pg.evaluate("document.fonts.ready")
    return ctx, pg


with sync_playwright() as p:
    b = p.chromium.launch(executable_path=args.browser, headless=True,
                          args=["--no-first-run", "--disable-background-networking", "--proxy-server=127.0.0.1:9"])
    for name, vp, ids in (("desk", {"width": 1280, "height": 860}, desk), ("phone", {"width": 390, "height": 844}, phone)):
        # Reduced motion renders each scene's final state at once, so captures are deterministic.
        ctx, pg = page(b, vp, "reduce", name)
        sw = pg.evaluate("document.documentElement.scrollWidth")
        if sw > vp["width"]:
            log.append(f"{name}: horizontal overflow, scrollWidth {sw}")
        for i, sid in enumerate(ids):
            if only and sid not in only:
                continue
            if sid == "top":
                pg.evaluate("scrollTo(0, 0)")
            elif sid == "end":
                pg.evaluate("scrollTo(0, document.documentElement.scrollHeight)")
            else:
                block = "center" if name == "desk" and sid not in ("engine", "data") else "start"
                pg.evaluate(f"document.querySelector('{selector(sid)}').scrollIntoView({{block: '{block}'}})")
            pg.wait_for_timeout(450)
            pg.screenshot(path=str(out / f"{name}-dark-{i:02d}-{sid}.png"))
        if args.full and name == "phone":
            pg.screenshot(path=str(out / "phone-dark-full.png"), full_page=True)
        ctx.close()
    if args.live:
        ctx, pg = page(b, {"width": 1280, "height": 860}, "no-preference", "live")
        for sid in live:
            if only and sid not in only:
                continue
            pg.evaluate(f"document.querySelector('#{sid}').scrollIntoView({{block: 'center', behavior: 'instant'}})")
            pg.wait_for_timeout(400)
            try:
                # The island turns into the live activity when the scene reaches its hand-off line.
                pg.wait_for_selector(".island:not(.is-live)", timeout=5000)
                pg.wait_for_selector(".island.is-live", timeout=45000)
                pg.wait_for_timeout(900)
            except Exception:
                log.append(f"live: no hand-off seen in #{sid}")
            pg.screenshot(path=str(out / f"live-{sid}.png"))
        if not only or "gauge" in only:
            pg.evaluate("scrollTo(0, 0)")
            pg.wait_for_timeout(300)
            pg.evaluate("document.querySelector('#engine .chart').scrollIntoView({block: 'center', behavior: 'instant'})")
            pg.wait_for_timeout(420)
            pg.screenshot(path=str(out / "live-gauge.png"))
        ctx.close()
    b.close()
srv.shutdown()
print("\n".join(log) or "no console errors, no overflow")
