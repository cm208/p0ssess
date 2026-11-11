#!/usr/bin/env python3
"""Check a built docs site: every internal link and anchor resolves, and nothing loads from elsewhere."""

import html.parser
import sys
from pathlib import Path
from urllib.parse import unquote, urlsplit


class Collect(html.parser.HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.ids, self.links, self.loads = set(), [], []

    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if a.get("id"):
            self.ids.add(a["id"])
        if tag == "a" and "href" in a:
            self.links.append(a["href"])
        elif tag == "link" and "href" in a:
            if a.get("rel") in ("canonical",):
                return
            self.loads.append(a["href"])
        elif tag in ("script", "img", "source", "iframe") and "src" in a:
            self.loads.append(a["src"])


def target_file(root, page, path):
    if path.startswith("/"):
        t = root / path.lstrip("/")
    else:
        t = (page.parent / path)
    if path.endswith("/") or t.is_dir():
        t = t / "index.html"
    return t.resolve()


def check(root):
    root = Path(root).resolve()
    pages = {}
    for f in sorted(root.rglob("*.html")):
        c = Collect()
        c.feed(f.read_text())
        pages[f.resolve()] = c
    problems = []
    for f, c in pages.items():
        name = f.relative_to(root)
        for url in c.loads:
            u = urlsplit(url)
            if u.scheme or u.netloc:
                problems.append("%s: loads from elsewhere: %s" % (name, url))
            elif not target_file(root, f, unquote(u.path)).is_file():
                problems.append("%s: missing file: %s" % (name, url))
        for url in c.links:
            u = urlsplit(url)
            if u.scheme or u.netloc:
                continue
            t = target_file(root, f, unquote(u.path)) if u.path else f
            if not t.is_file():
                problems.append("%s: broken link: %s" % (name, url))
                continue
            if u.fragment and t.suffix == ".html":
                ids = pages[t].ids if t in pages else set()
                if unquote(u.fragment) not in ids:
                    problems.append("%s: missing anchor: %s" % (name, url))
    return problems


if __name__ == "__main__":
    probs = check(sys.argv[1] if len(sys.argv) > 1 else Path(__file__).resolve().parents[2] / "site-docs-dist")
    print("\n".join(probs) if probs else "links ok")
    sys.exit(1 if probs else 0)
