# p0ssess website

A single static page: `index.html`, `style.css`, `scenes.js`, `favicon.svg` and `fonts/`. No build step, no framework, and no requests to anything but its own files.

## Preview locally

Browsers refuse web fonts over `file://`, so serve the folder:

```sh
python3 -m http.server --bind 127.0.0.1 --directory site 8000
```

Then open <http://127.0.0.1:8000/>. The pinned terminal appears at 1024 px and wider; narrower screens show each scene inline under its section. With `prefers-reduced-motion: reduce`, scenes show their final state with no typing.

## Screenshots

`shoot.py` serves the folder on 127.0.0.1, blocks every other request, and captures 1280×860 (each section) and 390×844 in both themes, with reduced motion so the captures are deterministic. It needs Playwright for Python and a Chromium-based browser:

```sh
python -I site/shoot.py --browser /path/to/chromium --out site/shots
```

It prints console errors and any horizontal overflow. The PNGs in `shots/` were reduced to 256-color palettes afterwards to keep them small.

## How the scenes work

Each section holds its scene as plain markup in a `<figure class="scene">`. That markup is the transcript screen readers get, the inline terminal on phones, and the script for the pinned terminal, which replays it when the section crosses the middle of the viewport.

| Markup | Meaning |
|---|---|
| `data-title` on the figure | terminal title |
| `data-st="state\|text"` on the figure | status segment at the start: `idle`, `run` or `amber`; empty text hides it |
| `data-ph` on the figure | placeholder in the input box |
| `p.l.in` | user input, typed into the input box; add `bang` for `!` shell mode |
| `p.l.ai`, `p.l.tool`, `p.l.sys` | Claude's text, a tool call, a dim notice |
| `p.l.res` | a tool result; `c` continues the previous line, `dim` greys it |
| `p.l.out` | raw command output |
| `data-d` on a line | delay before it appears, in ms |
| `data-st` on a line | status segment after it appears |

The `.foot` inside each figure is the final status line for the static version; keep it in step with the last `data-st`.

Transcripts must match what the plugin does (see `docs/`), and any cost shown must come from a measured run.

## Deploy

Copy `index.html`, `style.css`, `scenes.js`, `favicon.svg` and `fonts/` to any static host. `README.md`, `shoot.py` and `shots/` are not needed there.

The page carries a strict Content Security Policy in a `<meta>` tag. If the host can set headers, send the same policy as a header and add what a meta tag cannot express:

```
Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'
Referrer-Policy: no-referrer
X-Content-Type-Options: nosniff
```

Serve `.woff2` as `font/woff2` and cache `fonts/` for a long time. The links point at no public repository yet; add one when the repository is published.

## Fonts

Atkinson Hyperlegible Next and Atkinson Hyperlegible Mono (Braille Institute, via Google Fonts), and the single glyph ᗣ from Noto Sans Canadian Aboriginal for the status-line mark, each subset to what the page uses. All three are under the SIL Open Font License 1.1; the license texts are in `fonts/`.
