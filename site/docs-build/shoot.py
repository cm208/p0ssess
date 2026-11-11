#!/usr/bin/env python3
"""Screenshots of the built docs, plus console errors, outside requests and horizontal overflow.

Needs Playwright for Python and a Chromium-based browser:
  python3 shoot.py --browser /usr/bin/brave-browser --out /tmp/shots [--dist ../../site-docs-dist]
"""

import argparse
import functools
import http.server
import threading
from pathlib import Path

from playwright.sync_api import sync_playwright

HERE = Path(__file__).resolve().parent
SIZES = {"desktop": (1440, 900), "phone": (390, 844)}
SHOTS = {"overview": "/", "install": "/install/", "engine": "/engine/", "not-found": "/404.html"}
# Extra captures, scrolled to an element: (name, path, selector)
DETAIL = [
    ("engine-config", "/engine/", "#config-files"),
    ("engine-try", "/engine/", "#try-it"),
    ("install-notes-table", "/install-notes/", ".table-wrap"),
    ("research-index", "/research/", "#index"),
    ("design-routing", "/design/", "#how-a-request-is-routed"),
    ("media-results", "/media/", "#live-results-2026-11-09"),
]


class Quiet(http.server.SimpleHTTPRequestHandler):
    def log_message(self, *a):
        pass


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--browser", default="/usr/bin/brave-browser")
    ap.add_argument("--dist", default=str(HERE.parent.parent / "site-docs-dist"))
    ap.add_argument("--out", required=True)
    args = ap.parse_args()
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)

    handler = functools.partial(Quiet, directory=args.dist)
    srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    base = "http://127.0.0.1:%d" % srv.server_port
    pages = ["/"] + sorted("/%s/" % p.parent.name for p in Path(args.dist).glob("*/index.html")) + ["/404.html"]
    problems = []

    with sync_playwright() as pw:
        browser = pw.chromium.launch(executable_path=args.browser, args=["--no-first-run"])
        for size, (w, h) in SIZES.items():
            ctx = browser.new_context(viewport={"width": w, "height": h}, device_scale_factor=1, reduced_motion="reduce")
            page = ctx.new_page()

            def route(r):
                if r.request.url.startswith(base):
                    r.continue_()
                else:
                    problems.append("%s: outside request %s" % (size, r.request.url))
                    r.abort()

            page.route("**/*", route)
            page.on("console", lambda m: m.type in ("error", "warning") and problems.append("%s console %s: %s" % (size, m.type, m.text)))
            page.on("pageerror", lambda e: problems.append("%s page error: %s" % (size, e)))
            page.on("response", lambda r: r.status >= 400 and not r.url.endswith("/404.html") and problems.append("%s %d %s" % (size, r.status, r.url)))
            for path in pages:
                page.goto(base + path, wait_until="networkidle")
                page.evaluate("document.fonts.ready")
                over = page.evaluate("""() => {
                  const w = document.documentElement.clientWidth;
                  const wide = [...document.querySelectorAll('body *')].filter(e => {
                    const r = e.getBoundingClientRect();
                    if (r.right <= w + 0.5) return false;
                    for (let p = e.parentElement; p; p = p.parentElement) {
                      const o = getComputedStyle(p).overflowX;
                      if (o === 'auto' || o === 'scroll' || o === 'hidden') return false;
                    }
                    return true;
                  }).slice(0, 5).map(e => e.tagName.toLowerCase() + (e.className ? '.' + e.className : ''));
                  return {scroll: document.documentElement.scrollWidth, w, wide};
                }""")
                if over["scroll"] > over["w"] or over["wide"]:
                    problems.append("%s %s: horizontal overflow %s" % (size, path, over))
            for name, path in SHOTS.items():
                page.goto(base + path, wait_until="networkidle")
                page.evaluate("document.fonts.ready")
                page.screenshot(path=str(out / ("%s-%s.png" % (name, size))))
            for name, path, sel in DETAIL:
                page.goto(base + path, wait_until="networkidle")
                page.evaluate("document.fonts.ready")
                page.evaluate("(s) => document.querySelector(s).scrollIntoView({block: 'start'})", sel)
                page.screenshot(path=str(out / ("%s-%s.png" % (name, size))))
            ctx.close()
        browser.close()
    srv.shutdown()
    print("\n".join(problems) if problems else "no console errors, outside requests or overflow")
    print("shots in", out)


if __name__ == "__main__":
    main()
