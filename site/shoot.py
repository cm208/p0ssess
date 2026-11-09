"""Screenshot the site with Playwright and a Chromium-based browser, offline.
Usage: python -I site/shoot.py [--browser PATH] [--out DIR] [--full]
Serves site/ on 127.0.0.1 (browsers refuse web fonts over file://) and aborts every
other request. Console errors and horizontal overflow are printed."""
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
args = ap.parse_args()
out = Path(args.out)
out.mkdir(parents=True, exist_ok=True)
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
phone = ["top", "engine", "gauge", "media-cmp", "chores-cmp", "review-cmp", "usage-cmp", "stakes-cmp", "data"]
# Shots that start at a part of a section rather than the section itself.
parts = {"gauge": "#engine .gauge", "gates": "#engine .gates", "media-cmp": "#cmp-media", "chores-cmp": "#cmp-chores", "review-cmp": "#cmp-review", "stakes-cmp": "#cmp-stakes"}

with sync_playwright() as p:
    b = p.chromium.launch(executable_path=args.browser, headless=True,
                          args=["--no-first-run", "--disable-background-networking", "--proxy-server=127.0.0.1:9"])
    for scheme in ("dark",):
        for name, vp, ids in (("desk", {"width": 1280, "height": 860}, desk), ("phone", {"width": 390, "height": 844}, phone)):
            # Reduced motion renders each scene's final state at once, so captures are deterministic.
            ctx = b.new_context(viewport=vp, color_scheme=scheme, reduced_motion="reduce", device_scale_factor=1)
            ctx.route("**/*", lambda r: r.continue_() if r.request.url.startswith(origin + "/") else r.abort())
            pg = ctx.new_page()
            pg.on("console", lambda m, t=f"{scheme}-{name}": log.append(f"{t}: {m.type}: {m.text}") if m.type in ("error", "warning") else None)
            pg.on("pageerror", lambda e, t=f"{scheme}-{name}": log.append(f"{t}: pageerror: {e}"))
            pg.goto(url)
            pg.evaluate("document.fonts.ready")
            sw = pg.evaluate("document.documentElement.scrollWidth")
            if sw > vp["width"]:
                log.append(f"{scheme}-{name}: horizontal overflow, scrollWidth {sw}")
            for i, sid in enumerate(ids):
                if args.only and sid not in args.only.split(","):
                    continue
                if sid == "top":
                    pg.evaluate("scrollTo(0, 0)")
                elif sid == "end":
                    pg.evaluate("scrollTo(0, document.documentElement.scrollHeight)")
                else:
                    sel = parts.get(sid, "#" + sid)
                    if sid.endswith("-cmp"):
                        sel = f"#{sid[:-4]} .cmp"
                    block = "center" if name == "desk" and sid not in ("engine", "data") else "start"
                    pg.evaluate(f"document.querySelector('{sel}').scrollIntoView({{block: '{block}'}})")
                pg.wait_for_timeout(450)
                pg.screenshot(path=str(out / f"{name}-{scheme}-{i:02d}-{sid}.png"))
            if args.full and name == "phone":
                pg.screenshot(path=str(out / f"phone-{scheme}-full.png"), full_page=True)
            ctx.close()
    b.close()
srv.shutdown()
print("\n".join(log) or "no console errors, no overflow")
