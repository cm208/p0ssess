# p0ssess website

A single static page: `index.html`, `style.css`, `scenes.js`, `release.js`, `ghost.svg` and `fonts/`. No build step, no framework, and no requests to anything but its own files.

The page has three parts: a full-width hero with the hand-off drawn as a looping animation, the story (a column of sections beside a pinned terminal), and an ordinary install section.

## Preview locally

Browsers refuse web fonts over `file://`, so serve the folder:

```sh
python3 -m http.server --bind 127.0.0.1 --directory site 8000
```

Then open <http://127.0.0.1:8000/>. The page is dark only. In the story, the pinned terminal and the section island appear at 1024 px and wider; narrower screens put each session in a "Show the session" disclosure under its section. The two-column layout is chosen in CSS with `(scripting: enabled)`, so it is in place at first paint and nothing moves when the script loads; with scripts off, or in a browser without that media feature, the story stays one column with the disclosures.

With `prefers-reduced-motion: reduce`, or with scripts off, the hero is a still frame (the Haiku possessed, both wires lit, one measured job named), scenes show their final state with no typing, chart bars are drawn at full length, the ghost does not blink and the island cross-fades instead of morphing.

## Screenshots

`shoot.js` serves the folder on 127.0.0.1, blocks every other request, and captures 1280×860 (the hero, then each section) and 390×844 (the hero, the possession section, the engine, each cost chart, the use cases and the install section), with reduced motion so the captures are deterministic. `--only name,name` retakes single shots. `--live` adds captures with motion allowed (`live-*.png`): the hero's hand-off frozen at a fixed moment of its loop, on desktop and phone; the engine chart while its bars grow; and each scene that hands work off, taken while the island shows it. It needs Node, `playwright-core` and a Chromium-based browser:

```sh
node site/shoot.js --browser /path/to/chromium --out site/shots --live
```

Without an installed `playwright-core`, set `PLAYWRIGHT_CORE` to a copy of it; Playwright for Python ships one as `playwright/driver/package`, next to its own `node`.

It prints console errors and any horizontal overflow. The PNGs in `shots/` were reduced to 256-color palettes afterwards (`magick in.png -colors 256 PNG8:out.png`) to keep them small.

## The hero

The mark is `ghost.svg`, a copy of `../assets/ghost.svg`. It is the tab icon, the lockup, the footer mark and both ghosts in the hand-off, so replacing that one file changes all of them. It is drawn on a 16-unit grid, like the wordmark's pixels, and every use is a whole multiple of the unit (`--u`) so it stays crisp. One thing depends on its drawing: the blink covers the top of the two slit eyes with the mask's color, at columns 6 and 9, rows 5 and 6 (`.g .lid` and `--ghost-mask` in `style.css`). Move those if the eyes move.

The hand-off (`figure.relay`) is three stops on one line: Claude, the possessed Haiku, and another model. The loop is CSS, eight seconds, with every part on the same clock; the comment above the keyframes in `style.css` gives the timeline. `scenes.js` measures where the riding ghost lands (the seat in the Haiku pill), names the next job each time round, runs the Pause button and stops the loop while the hero is off screen.

The jobs it names are in `data-jobs` on the figure, and the same three are in its screen-reader caption; change both together. Each is a measured call from the live results in `../docs/MEDIA.md`: a ten-second clip on gemini-3.8-flash for $0.0067, 16 seconds of speech on whisper-large-v3-turbo for $0.0002, and a 1024-pixel image on recraft-v4.1-flash for $0.0070. The possession section's figures are from one measured 0.5.0 run of "what's in this video?": $0.001 for the Haiku and $0.0003 for the model that watched, with 10 input and 791 output tokens in the main session beyond its fixed system prompt, against six turns and about 155,000 cached tokens for the same request in 0.4.

## Going live

`release.js` is the one place the page reads addresses from. As committed, `repo` and `version` are empty: the download buttons are disabled, the install command shows `<repository URL>`, and the Source, Docs and release links are hidden or inert. To switch them on:

1. Set `repo` to the repository's page on the git server, without a trailing slash (`https://<host>/<owner>/p0ssess`). The plan is the project's Gitea at git.christianmoo.re. This turns on "Source", "View the source", and the real address in the install command (`<repo>.git`).
2. Publish the release there (see `../docs/RELEASE.md`), then set `version` to it, without the `v`. The four download buttons, `SHA256SUMS` and "Release notes" then point at `<repo>/releases/download/v<version>/<file>` and `<repo>/releases/tag/v<version>`, which is where Gitea serves a release. The files are `p0ssess-linux-amd64`, `p0ssess-linux-arm64`, `p0ssess-darwin-arm64`, `p0ssess-<version>.tar.gz` and `SHA256SUMS`.
3. Optionally paste that release's three sums into `sha256` (from its `SHA256SUMS`, which is `../dist/SHA256SUMS` at the tag) to print each under its file. Never type one from memory.
4. Set `docs` when the documentation site is up (planned at docs.p0ssess.me) to show the Docs link.
5. In `index.html`, change "Private for now" in the hero and the install section, and add a canonical link and `og:url` for the site's address (planned at p0ssess.me).

With scripts off the buttons stay disabled, because the addresses live in the script.

## How the scenes work

Each section of the story holds its scene as plain markup in a `<figure class="scene">` inside a `<details class="scene-wrap">`. That markup is the transcript screen readers get, the inline terminal on phones, and the script for the pinned terminal, which replays it when the section crosses the middle of the viewport. The install section has one more scene, the first run, in a disclosure of its own at every width.

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
| `span.nb` in a `p.l.out` | a clause that stays together when a wide output row wraps (the engine's `ask: …`) |
| `ann` on a line | work leaves for the possessed Haiku here (an `Agent(p0ssess:p0ssess, …)` call or a message to it); the island becomes a live activity until the next line |
| `data-credit` on a line | the result is back; the island shows this credit (the model and its cost) for a moment |
| `blk` on `p.l.ai` lines | a code block in Claude's reply, such as the `/p0ssess:p0ssess-me` summary |
| the `figcaption` | read to screen readers as is; shown under the pinned terminal without its "Transcript:" prefix |

The `.foot` inside each figure is the final status line for the static version; keep it in step with the last `data-st`.

Transcripts must match what the plugin does (see `docs/`). In 0.5 every routed call goes through the `p0ssess:p0ssess` agent, so a hand-off is one `Agent(p0ssess:p0ssess, "…")` line and then the answer ending in its credit, `(model, cost)`; main Claude never calls `direct`, `run` or `recommend` itself. Where each scene comes from:

- Lines starting with `!` are real command output from the 0.5.1 binary in a scratch `P0SSESS_HOME`: the two `engine recommend` runs and `engine set plan subscription` in the engine scene, `engine recommend --use-case video-understand` and `engine usecases | wc -l` in the use-case scene.
- The first-run summary in the install section is `p0ssess me --brief` from 0.5.1 in a fresh scratch home with an empty Claude config, with the long home path and the page token shortened. The summary in the status-line section is the example in `../docs/ENGINE.md`, with the version that binary prints.
- The hand-offs follow `../agents/p0ssess.md` and `../skills/routing/SKILL.md`: the one-sentence agent prompt, the answer with its credit, a diff that comes back as a path for Claude to read and apply, a permission request answered through the agent, and the approval flow. The Agent line is written the way a headless run prints it. `SendMessage(p0ssess:p0ssess, "…")` follows the same form and has not been captured from a run. Costs in credits are measured (whisper for 16 seconds of speech, Gemini 3.8 Flash for a ten-second clip, both in `../docs/MEDIA.md`) or the engine's estimate for that route (the coding and review hand-offs, the $0.74 in the usage scene).

Costs in the charts come from `p0ssess engine recommend --json`: a score is 100 × (1 − `p_fail_upper`), a cost is the option's `total_usd`, rounded. The engine example is `--use-case pdf-table-extract` and the media one `--use-case audio-transcribe --audio-seconds 16.2`; the coding and review charts are `migration-test-gated` and `code-review-diff`. All four run in a scratch `P0SSESS_HOME` with `plan = "api"` and no history, and 0.5.1 gives the same figures as 0.4.0. The usage example feeds two status-line readings through `p0ssess statusline` in a scratch `P0SSESS_HOME`, then runs `--use-case long-agent-session`.

## How the charts are drawn

Each chart is an `ol.bars` in a `figure.chart`. Rows are sorted by total and the winner is first, with a visible "lowest total" tag, so the result does not depend on color. A row holds the name, the total, the bar, and under it the score against the gate and the price of the call alone. A chart shows only some of the engine's ranked options, and its caption says how many (`--top 200 --json` gives the count).

Each bar is an SVG `rect` whose `width` attribute is a percentage, since the CSP forbids inline styles:

- Everywhere but the media chart, the width is the total as a share of the largest total in that chart, from a zero baseline. The two usage charts share one scale (the late Claude total).
- The media chart's three totals are $0.0701, $0.0748 and $0.0756, which a zero baseline cannot tell apart. Every option there carries the same $0.055 of Claude time (`overhead` 0.05 plus `review` 0.005 in the JSON breakdown), so its scale starts at $0.055 and ends at $0.08: width = (total − 0.055) / 0.025. The bars carry a break mark, the axis under them is labeled from $0.055, and a line under the axis says so. Keep all three if the numbers change.

With motion allowed, the bars grow once when a chart scrolls into view, each at about the same speed, so the shortest finishes first and then gets its tag.

The gate table draws the 0 to 100 score as the rule under each row; the classes `g20`, `g35`, `g60` and `g80` place the violet part, which is the scores that clear that gate.

## Deploy

Copy `index.html`, `style.css`, `scenes.js`, `release.js`, `ghost.svg` and `fonts/` to any static host. `README.md`, `shoot.js` and `shots/` are not needed there.

The page carries a strict Content Security Policy in a `<meta>` tag. If the host can set headers, send the same policy as a header and add what a meta tag cannot express:

```
Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'
Referrer-Policy: no-referrer
X-Content-Type-Options: nosniff
```

Serve `.woff2` as `font/woff2` and cache `fonts/` for a long time. Outside addresses are only followed when someone clicks a link; the page itself loads nothing from them.

## Fonts

Obel Mono (headings, numbers, terminal; instanced to weights 400–700 and subset), IBM Plex Sans 400 (body), and the single glyph ᗣ from Noto Sans Canadian Aboriginal for the status-line mark. All are under the SIL Open Font License 1.1; the license texts are in `fonts/`.
