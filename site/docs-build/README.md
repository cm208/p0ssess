# p0ssess docs site

The documentation site (planned at docs.p0ssess.me) is generated from the Markdown in the repository: `README.md` is the overview and each page in `docs/` is one page. Edit the Markdown, never the output.

## Build

```sh
python3 site/docs-build/build.py
```

Python 3.11 or later, standard library only. The output goes to `site-docs-dist/` at the repository root (not committed; it is rebuilt from the sources). The build stops on a broken link in the Markdown, then checks the output: every internal link and anchor must resolve and no page may load anything from another host. `python3 site/docs-build/check.py [dir]` runs that check on its own.

Without a suitable Python, the same build in a pinned container gives byte-identical output:

```sh
podman run --rm -v "$PWD":/src:z -w /src docker.io/library/python:3.13.17-slim python3 site/docs-build/build.py
```

`--out DIR` writes elsewhere (the build replaces that directory only if it is empty or holds a previous build), and `--base-url URL` overrides the address for a staging host.

## Configuration

`config.toml` holds the addresses and the page list:

- `base_url`: the docs site, for canonical links, `sitemap.xml` and `robots.txt`.
- `site_url`: the landing page, linked from the header.
- `repo_url` and `branch`: a Markdown link to a repository file that is not a page (code, `LICENSE`, `catalog/`) goes to `<repo_url>/src/branch/<branch>/<path>`.
- `pages`: source, slug, sidebar label and group, in reading order. Links between listed files become links between pages, anchors included; a page's own `#anchor` links keep working because headings get GitHub-style ids. To add a page, add its line.

## What it makes

Each page is `<slug>/index.html`, plus `404.html`, `sitemap.xml`, `robots.txt`, `docs.css`, `docs.js`, `ghost.svg` (the mark and favicon) and `fonts/`. Links between pages are relative; only `404.html` uses root paths, since the server returns it at any depth.

The look is the landing page's: `docs.css` starts with the `@font-face` rules and `:root` tokens copied from `site/style.css` at build time, the fonts and `ghost.svg` are copied from `site/`, and the wordmark in `templates/page.html` is the one in `site/index.html`. Dark only.

`docs.js` adds a copy button to each code block and marks the current section in "On this page". Without it the pages work the same, minus those two. Asset URLs carry a content hash (`docs.css?v=…`), so they can be cached for a long time.

`markdown.py` renders CommonMark plus GFM tables and bare-URL links, the subset these docs use. Raw HTML blocks keep their text and drop attributes and images (the README's title block); inline HTML is shown as text. On the docs as of 0.5.2 its output matches markdown-it-py 4.3.0's CommonMark renderer with tables, apart from the bare-URL links.

## Serving

Any static server. For nginx: `index index.html;`, `error_page 404 /404.html;`, `font/woff2` for `.woff2`, and the policy the pages carry in a meta tag as headers, plus what a meta tag cannot set:

```
Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'
Referrer-Policy: no-referrer
X-Content-Type-Options: nosniff
```

## Screenshots

`shoot.py` serves the output on 127.0.0.1, takes the overview, install, engine and 404 pages at 1440×900 and 390×844 plus a few scrolled details, and reports console errors, requests to other hosts and horizontal overflow on every page. It needs Playwright for Python and a Chromium-based browser:

```sh
python3 site/docs-build/shoot.py --browser /usr/bin/brave-browser --out /tmp/docs-shots
```
