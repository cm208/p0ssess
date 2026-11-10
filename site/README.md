# p0ssess website

A single static page: `index.html`, `style.css`, `scenes.js`, `ghost.svg` and `fonts/`. No build step, no framework, and no requests to anything but its own files.

## Preview locally

Browsers refuse web fonts over `file://`, so serve the folder:

```sh
python3 -m http.server --bind 127.0.0.1 --directory site 8000
```

Then open <http://127.0.0.1:8000/>. The page is dark only. The pinned terminal and the section island appear at 1024 px and wider; narrower screens put each session in a "Show the session" disclosure under its section. The two-column layout is chosen in CSS with `(scripting: enabled)`, so it is in place at first paint and nothing moves when the script loads; with scripts off, or in a browser without that media feature, the page stays one column with the disclosures. With `prefers-reduced-motion: reduce`, scenes show their final state with no typing, chart bars are drawn at full length, the ghost does not blink and the island cross-fades instead of morphing.

## Screenshots

`shoot.py` serves the folder on 127.0.0.1, blocks every other request, and captures 1280×860 (each section) and 390×844 (the opening, the engine, each cost chart, the use cases and the setup steps), with reduced motion so the captures are deterministic. `--only name,name` retakes single shots. `--live` adds desktop captures with motion allowed (`live-*.png`): each scene that hands work off, taken while the island shows it, and the engine chart. It needs Playwright for Python and a Chromium-based browser:

```sh
python -I site/shoot.py --browser /path/to/chromium --out site/shots --live
```

It prints console errors and any horizontal overflow. The PNGs in `shots/` were reduced to 256-color palettes afterwards (`magick in.png -colors 256 PNG8:out.png`) to keep them small.

## How the scenes work

Each section holds its scene as plain markup in a `<figure class="scene">` inside a `<details class="scene-wrap">`. That markup is the transcript screen readers get, the inline terminal on phones, and the script for the pinned terminal, which replays it when the section crosses the middle of the viewport.

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
| `data-me` on the figure or a line | the user's own status line text (the usage scene shows a 5-hour reading there) |
| `ann` on a line | a "handing this to …" line; the island shows it as a live activity |
| `span.nb` in a `p.l.out` | a clause that stays together when a wide output row wraps (the engine's `ask: …`) |
| the `figcaption` | read to screen readers as is; shown under the pinned terminal without its "Transcript:" prefix |

The `.foot` inside each figure is the final status line for the static version; keep it in step with the last `data-st`.

Transcripts must match what the plugin does (see `docs/`). Costs come from measured runs, or from `p0ssess engine recommend --json` for the cost charts: a score is 100 × (1 − `p_fail_upper`), a cost is the option's `total_usd`, rounded. The engine example is `--use-case pdf-table-extract` and the media one `--use-case audio-transcribe --audio-seconds 16.2`; the coding and review charts are `migration-test-gated` and `code-review-diff`. All four run in a scratch `P0SSESS_HOME` with `plan = "api"` and no history. The usage example feeds two status-line readings through `p0ssess statusline` in a scratch `P0SSESS_HOME`, then runs `--use-case long-agent-session`.

## How the charts are drawn

Each chart is an `ol.bars` in a `figure.chart`. Rows are sorted by total and the winner is first, with a visible "lowest total" tag, so the result does not depend on color. A row holds the name, the total, the bar, and under it the score against the gate and the price of the call alone. A chart shows only some of the engine's ranked options, and its caption says how many (`--top 200 --json` gives the count).

Each bar is an SVG `rect` whose `width` attribute is a percentage, since the CSP forbids inline styles:

- Everywhere but the media chart, the width is the total as a share of the largest total in that chart, from a zero baseline. The two usage charts share one scale (the late Claude total).
- The media chart's three totals are $0.0701, $0.0748 and $0.0756, which a zero baseline cannot tell apart. Every option there carries the same $0.055 of Claude time (`overhead` 0.05 plus `review` 0.005 in the JSON breakdown), so its scale starts at $0.055 and ends at $0.08: width = (total − 0.055) / 0.025. The bars carry a break mark, the axis under them is labeled from $0.055, and a line under the axis says so. Keep all three if the numbers change.

With motion allowed, the bars grow once when a chart scrolls into view, each at about the same speed, so the shortest finishes first and then gets its tag.

The gate table draws the 0 to 100 score as the rule under each row; the classes `g20`, `g35`, `g60` and `g80` place the violet part, which is the scores that clear that gate.

## Deploy

Copy `index.html`, `style.css`, `scenes.js`, `ghost.svg` and `fonts/` to any static host. `README.md`, `shoot.py` and `shots/` are not needed there.

The page carries a strict Content Security Policy in a `<meta>` tag. If the host can set headers, send the same policy as a header and add what a meta tag cannot express:

```
Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'
Referrer-Policy: no-referrer
X-Content-Type-Options: nosniff
```

Serve `.woff2` as `font/woff2` and cache `fonts/` for a long time. The links point at no public repository yet; add one when the repository is published.

## Fonts

Obel Mono (headings, numbers, terminal; instanced to weights 400–700 and subset), IBM Plex Sans 400 (body), and the single glyph ᗣ from Noto Sans Canadian Aboriginal for the status-line mark. All are under the SIL Open Font License 1.1; the license texts are in `fonts/`.
