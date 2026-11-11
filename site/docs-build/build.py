#!/usr/bin/env python3
"""Build the p0ssess docs site from the repository's Markdown. Python 3.11+, stdlib only."""

import argparse
import hashlib
import html
import html.parser
import os
import posixpath
import re
import shutil
import sys
import tempfile
import tomllib
from pathlib import Path
from string import Template

sys.dont_write_bytecode = True
HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent
sys.path.insert(0, str(HERE))

import check  # noqa: E402
import markdown  # noqa: E402

SCHEME = re.compile(r"^[A-Za-z][A-Za-z0-9+.-]*:|^//")


class HtmlBlock(html.parser.HTMLParser):
    """Keeps the text structure of a raw HTML block and drops attributes and images.

    The README opens with an HTML title block; the site header already carries the mark,
    so its image is left out."""

    KEEP = {"p", "h1", "h2", "h3", "h4", "h5", "h6", "em", "strong", "code", "br", "a"}

    def __init__(self, link_hook):
        super().__init__(convert_charrefs=True)
        self.out = []
        self.link_hook = link_hook

    def handle_starttag(self, tag, attrs):
        if tag == "a":
            href = dict(attrs).get("href")
            self.out.append('<a href="%s">' % markdown.esc(self.link_hook(href)) if href else "<a>")
        elif tag in self.KEEP:
            self.out.append("<%s>" % tag)

    def handle_endtag(self, tag):
        if tag in self.KEEP and tag != "br":
            self.out.append("</%s>" % tag)

    def handle_data(self, data):
        self.out.append(markdown.esc(data))

    def result(self):
        s = "".join(self.out)
        s = re.sub(r"<p>\s*</p>", "", s)
        return s.strip()


def page_dir(slug):
    return slug or "."


def rel(from_slug, to_slug):
    r = posixpath.relpath(page_dir(to_slug), page_dir(from_slug))
    return "./" if r == "." else r + "/"


def describe(text, limit=160):
    text = re.sub(r"\s+", " ", text).strip()
    if len(text) <= limit:
        return text
    out = ""
    for s in re.split(r"(?<=[.!?])\s+(?=[A-Z0-9\"“(])", text):
        if len(out) + len(s) + 1 > limit:
            break
        out = (out + " " + s).strip()
    if out:
        return out
    cut = text[: limit - 1].rsplit(" ", 1)[0].rstrip(",;:")
    return cut + "…"


def short_hash(data):
    return hashlib.sha256(data).hexdigest()[:10]


def tokens_from_landing():
    """The landing page's @font-face rules and :root tokens, copied verbatim."""
    css = (REPO / "site" / "style.css").read_text()
    faces = re.findall(r"@font-face\s*\{[^}]*\}", css)
    root = re.search(r":root\s*\{[^}]*\}", css)
    if len(faces) < 3 or not root:
        sys.exit("build: could not find the font faces and :root tokens in site/style.css")
    return "/* From site/style.css, copied at build time. */\n" + "\n".join(faces) + "\n\n" + root.group(0) + "\n"


class Site:
    def __init__(self, cfg, base_url):
        self.cfg = cfg
        self.base = base_url.rstrip("/")
        self.repo_url = cfg["repo_url"].rstrip("/")
        self.src_url = "%s/src/branch/%s" % (self.repo_url, cfg["branch"])
        self.pages = cfg["pages"]
        self.by_source = {p["source"]: p for p in self.pages}
        self.errors = []
        self.files = {}  # output path -> source path, for local images
        vfile = REPO / "dist" / "VERSION"
        self.version = vfile.read_text().split()[0] if vfile.exists() else ""

    def link_hook(self, page):
        src = page["source"]

        def hook(url):
            if not url or url.startswith("#") or SCHEME.match(url):
                return url
            path, _, frag = url.partition("#")
            target = posixpath.normpath(posixpath.join(posixpath.dirname(src), path))
            if target.startswith(".."):
                self.errors.append("%s: link outside the repository: %s" % (src, url))
                return url
            frag = "#" + frag if frag else ""
            if target in self.by_source:
                to = self.by_source[target]["slug"]
                if to == page["slug"] and frag:
                    return frag
                return rel(page["slug"], to) + frag
            if not (REPO / target).exists():
                self.errors.append("%s: broken link: %s" % (src, url))
            return "%s/%s%s" % (self.src_url, target, frag)

        return hook

    def image_hook(self, page):
        src = page["source"]

        def hook(url):
            if SCHEME.match(url):
                self.errors.append("%s: remote image (blocked by the CSP): %s" % (src, url))
                return url
            target = posixpath.normpath(posixpath.join(posixpath.dirname(src), url))
            if not (REPO / target).is_file():
                self.errors.append("%s: missing image: %s" % (src, url))
                return url
            out = "files/" + target
            self.files[out] = REPO / target
            return posixpath.relpath(out, page_dir(page["slug"]))

        return hook

    def nav(self, current, root):
        out, group = [], None
        for p in self.pages:
            if p["group"] != group:
                if group is not None:
                    out.append("        </ul>")
                group = p["group"]
                out.append('        <p class="nav-k">%s</p>\n        <ul>' % html.escape(group))
            href = root + (p["slug"] + "/" if p["slug"] else "") if root == "/" else rel(current, p["slug"])
            cur = ' aria-current="page"' if p["slug"] == current else ""
            out.append('          <li><a href="%s"%s>%s</a></li>' % (href, cur, html.escape(p["nav"])))
        out.append("        </ul>")
        return "\n".join(out)

    def toc(self, headings, mobile):
        items = [(lvl, hid, re.sub(r"</?a[^>]*>", "", inner)) for lvl, hid, inner in headings if lvl in (2, 3)]
        if len(items) < 2:
            return ""
        lis, open_sub = [], False
        for lvl, hid, inner in items:
            if lvl == 3 and not open_sub and lis:
                lis[-1] = lis[-1][: -len("</li>")] + "\n<ul>"
                open_sub = True
            elif lvl == 2 and open_sub:
                lis.append("</ul></li>")
                open_sub = False
            lis.append('<li><a href="#%s">%s</a></li>' % (hid, inner))
        if open_sub:
            lis.append("</ul></li>")
        ul = "<ul>\n%s\n</ul>" % "\n".join(lis)
        if mobile:
            return '<details class="toc-m">\n<summary>On this page</summary>\n<nav aria-label="On this page">\n%s\n</nav>\n</details>' % ul
        return '<aside class="toc-d">\n<nav aria-label="On this page">\n<p class="nav-k">On this page</p>\n%s\n</nav>\n</aside>' % ul

    def pager(self, i):
        cur = self.pages[i]["slug"]
        parts = []
        if i > 0:
            p = self.pages[i - 1]
            parts.append('<a class="prev" href="%s" rel="prev"><span class="pg-k">Previous</span>%s</a>' % (rel(cur, p["slug"]), html.escape(p["nav"])))
        if i + 1 < len(self.pages):
            p = self.pages[i + 1]
            parts.append('<a class="next" href="%s" rel="next"><span class="pg-k">Next</span>%s</a>' % (rel(cur, p["slug"]), html.escape(p["nav"])))
        return '    <nav class="pager" aria-label="Previous and next">\n      %s\n    </nav>' % "\n      ".join(parts)

    def common(self, root):
        site_url = self.cfg["site_url"].rstrip("/")
        return {
            "root": root,
            "site_url": html.escape(site_url + "/"),
            "site_host": html.escape(re.sub(r"^https?://", "", site_url)),
            "repo_url": html.escape(self.repo_url),
            "version": html.escape(self.version),
        }

    def render_page(self, i, tpl, assets):
        page = self.pages[i]
        source = REPO / page["source"]
        text = source.read_text()
        link = self.link_hook(page)
        body, headings = markdown.render(
            text,
            link_hook=link,
            image_hook=self.image_hook(page),
            html_hook=lambda raw: self.html_block(raw, link),
        )
        m = re.search(r"<h1[^>]*>(.*?)</h1>", body, re.S)
        if not m:
            self.errors.append("%s: no h1" % page["source"])
            return ""
        h1 = markdown.plain_text(m.group(1)).strip()
        after = body[m.end():]
        pm = re.search(r"<p>(.*?)</p>", after, re.S)
        desc = describe(markdown.plain_text(pm.group(1))) if pm else h1
        mobile_toc = self.toc(headings, True)
        if mobile_toc:
            at = m.end() + pm.end() if pm and not after[: pm.start()].strip() else m.end()
            body = body[:at] + "\n" + mobile_toc + "\n" + body[at:]
        slug = page["slug"]
        url = "%s/%s" % (self.base, slug + "/" if slug else "")
        title = "p0ssess documentation" if not slug else "%s · p0ssess docs" % h1
        head = "\n".join([
            '<link rel="canonical" href="%s">' % url,
            '<meta property="og:title" content="%s">' % html.escape(h1 if slug else "p0ssess documentation"),
            '<meta property="og:description" content="%s">' % html.escape(desc),
            '<meta property="og:type" content="%s">' % ("article" if slug else "website"),
            '<meta property="og:url" content="%s">' % url,
            '<meta property="og:site_name" content="p0ssess">',
        ])
        root = rel(slug, "")
        root = "" if root == "./" else root
        vals = self.common(root)
        vals.update(assets)
        vals.update({
            "title": html.escape(title),
            "description": html.escape(desc),
            "head_extra": head,
            "home": rel(slug, ""),
            "nav": self.nav(slug, None),
            "nav_here": html.escape(page["nav"]),
            "doc_class": " home" if not slug else "",
            "body": body.rstrip(),
            "pager": self.pager(i),
            "source": '    <p class="src">This page is generated from <a href="%s/%s"><code>%s</code></a>.</p>' % (self.src_url, page["source"], page["source"]),
            "toc": self.toc(headings, False),
        })
        return tpl.substitute(vals)

    def html_block(self, raw, link):
        p = HtmlBlock(link)
        p.feed(raw)
        p.close()
        return p.result()

    def render_404(self, tpl, assets):
        vals = self.common("/")
        vals.update(assets)
        vals.update({
            "title": "Page not found · p0ssess docs",
            "description": "There is no page at this address.",
            "head_extra": '<meta name="robots" content="noindex">',
            "home": "/",
            "nav": self.nav(None, "/"),
            "nav_here": "",
            "doc_class": " lost",
            "body": '<h1 id="not-found">Page not found</h1>\n<p>There is no page at this address. It may have moved; every page is in the list, and the <a href="/">overview</a> is the place to start.</p>',
            "pager": "",
            "source": "",
            "toc": "",
        })
        return tpl.substitute(vals)

    def sitemap(self):
        urls = ["%s/%s" % (self.base, p["slug"] + "/" if p["slug"] else "") for p in self.pages]
        body = "\n".join("  <url><loc>%s</loc></url>" % html.escape(u) for u in urls)
        return '<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n%s\n</urlset>\n' % body


def write(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    if isinstance(data, str):
        data = data.encode()
    path.write_bytes(data)


def looks_like_output(d):
    return not any(d.iterdir()) or ((d / "sitemap.xml").exists() and (d / "404.html").exists())


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--config", default=str(HERE / "config.toml"))
    ap.add_argument("--out", default=str(REPO / "site-docs-dist"))
    ap.add_argument("--base-url", help="override base_url from the config (for a staging host)")
    args = ap.parse_args()

    cfg = tomllib.loads(Path(args.config).read_text())
    site = Site(cfg, args.base_url or cfg["base_url"])
    out = Path(args.out).resolve()
    if out.exists() and not looks_like_output(out):
        sys.exit("build: %s exists and does not look like a previous build; not touching it" % out)

    tpl = Template((HERE / "templates" / "page.html").read_text())
    css = (tokens_from_landing() + "\n" + (HERE / "templates" / "docs.css").read_text()).encode()
    js = (HERE / "templates" / "docs.js").read_bytes()
    assets = {"css_v": short_hash(css), "js_v": short_hash(js)}

    stage = Path(tempfile.mkdtemp(prefix="p0ssess-docs-"))
    try:
        for i, p in enumerate(site.pages):
            if not (REPO / p["source"]).is_file():
                sys.exit("build: missing source %s" % p["source"])
            slug = p["slug"]
            write(stage / (slug + "/index.html" if slug else "index.html"), site.render_page(i, tpl, assets))
        write(stage / "404.html", site.render_404(tpl, assets))
        write(stage / "docs.css", css)
        write(stage / "docs.js", js)
        write(stage / "ghost.svg", (REPO / "site" / "ghost.svg").read_bytes())
        for f in sorted((REPO / "site" / "fonts").iterdir()):
            if f.suffix in (".woff2", ".txt"):
                write(stage / "fonts" / f.name, f.read_bytes())
        for dst, srcf in sorted(site.files.items()):
            write(stage / dst, srcf.read_bytes())
        write(stage / "sitemap.xml", site.sitemap())
        write(stage / "robots.txt", "User-agent: *\nAllow: /\n\nSitemap: %s/sitemap.xml\n" % site.base)

        if site.errors:
            sys.exit("build: failed\n  " + "\n  ".join(site.errors))
        problems = check.check(stage)
        if problems:
            sys.exit("build: link check failed\n  " + "\n  ".join(problems))

        for root, dirs, files in os.walk(stage):
            os.chmod(root, 0o755)
            for f in files:
                os.chmod(os.path.join(root, f), 0o644)
        if out.exists():
            shutil.rmtree(out)
        shutil.move(str(stage), str(out))
    finally:
        if stage.exists():
            shutil.rmtree(stage)

    n = sum(1 for _ in out.rglob("*") if _.is_file())
    size = sum(f.stat().st_size for f in out.rglob("*") if f.is_file())
    print("built %d pages, %d files, %.0f KiB -> %s" % (len(site.pages), n, size / 1024, out))


if __name__ == "__main__":
    main()
