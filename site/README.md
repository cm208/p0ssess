# p0ssess website

A single static page: `index.html`, `style.css`, `page.js`, `release.js`, `ghost.svg`, three film images (`film-frames.webp`, `film-strip.webp`, `film-still.png`), `og.png` (the share image) and `fonts/`. No build step, no framework, and no requests to anything but its own files.

The page is a launch page. In order:

1. **The hero.** The headline, what p0ssess is, the way to the install steps, and beside them the film test: one prompt run twice on a ten-minute film, Claude alone and Claude with p0ssess.
2. **Three things Claude couldn't do before.** Video, audio and images, each with something to say to Claude and one measured call.
3. **How a possession works.** The hand-off drawn as three stops, and where one run's money went.
4. **An engine picks who does the job.** The gates and one ranking from the engine.
5. **The work Claude shouldn't spend itself on.** Chores, second opinions and usage-aware routing, with engine estimates.
6. **You set the limits.** Rules, the sandbox, the status line and what it won't do.
7. **Install.**

A pill dock (the island) is fixed to the top once the page scrolls. It holds a dot for each part, the name of the one in view, and a link to the install steps.

## Preview locally

Browsers refuse web fonts over `file://`, so serve the folder:

```sh
python3 -m http.server --bind 127.0.0.1 --directory site 8000
```

Then open <http://127.0.0.1:8000/>. The page is dark only. The hero is two columns from 1024 px, the frame and its caption sit side by side from 620 px, and narrower screens stack everything.

## Motion

One motion language: arrivals ease out (`--out`), travel that stays on screen eases in and out (`--io`), and the spring (`--spring`) is kept for the island, the two-way switches and a press. Nothing fades up as it scrolls in. What moves:

- **The sweep**, the page's one authored moment. With motion allowed the film test opens on "Claude alone" and, once its timeline is on screen, the ghost rides the playhead across the film in 2.4 seconds. The lit layers follow it, the frames run in the screen, and the score ticks from 15 to 25 as it passes what Claude alone missed.
- **Hairlines** draw out of their 24 px violet start when they scroll into view, and **chart bars** grow from the baseline, each once.
- **The hand-off** plays once when it scrolls into view (4.8 seconds) and again on Replay.
- **The island** slides its violet dot to the part in view and resizes to its name.

Every animated part has a complete still state in the markup. With `prefers-reduced-motion: reduce`, or with scripts off, the page shows that: the film test on "with p0ssess" at 25 of 25, the hand-off possessed with one measured job named, bars at full length. With reduced motion the switches, the event tabs and scrubbing still work, without the sweep. If scripting is on but `page.js` never runs, the film test shows itself after 2.5 seconds.

## The film test

Everything in the widget is from the comparison in `../docs/MEDIA.md` ("Ten minutes of video: Claude alone and with p0ssess", and "Run B on 0.5.4", attempt 2). Run A was Claude Sonnet 5.5 alone; run B added p0ssess 0.5.4, which sent the film to qwen3.8-omni-flash.

| On the page | Claude alone | With p0ssess | Source |
|---|---|---|---|
| Score | 15 of 25 | 25 of 25 | MEDIA.md, the run B on 0.5.4 table |
| Events | 12 of 20 | 20 of 20 | same |
| Sound cues | 3 of 5 | 5 of 5 | same |
| Total cost | $0.0999 | $0.1140 | same |
| Wall time | 40 s | 262 s | same |
| What watched | 20 stills, one every 30 s, and loudness readings | the whole film at 1 frame a second, with sound | MEDIA.md, "What happened"; the downscale step in "Limits and large videos" |

The $0.0141 in the note under the timeline is the difference between the two totals. The page says p0ssess cost more on this job and took longer, because it did. Do not write that it is cheaper.

**The events.** The ten events and their windows are the checklist written before either run. MEDIA.md lists the events; the windows are kept with the run's records and copied here. An event scores one point if described and one more if its time is within 30 seconds of the window. Which events run A found is from MEDIA.md's "Scores" paragraph.

| Event | Window | Claude alone | Frame shown (s) | Quoted from the answer, at |
|---|---|---|---|---|
| Opening and title cards | 0:00–0:30 | found | 17 | 0:16–0:23 |
| The bunny wakes | 0:30–0:55 | found | 48 | 0:32–0:57 |
| His morning | 0:55–1:55 | found | 78 | 0:57–1:38 |
| Three rodents appear | 1:55–2:30 | found | 120 | 1:52–2:26 |
| They pelt him with fruit and nuts | 2:30–3:10 | missed | 176 | 2:26–3:01 |
| They kill the butterflies | 3:05–3:45 | missed | 204 | 3:01–3:51 |
| He makes weapons and traps | 3:45–5:00 | found | 262.5 | 4:02–4:45 |
| The ambush | 5:00–6:10 | missed | 347 | 5:40–5:56 |
| The flying squirrel is caught | 6:10–8:05 | missed | 448 | 6:44–7:26 |
| Credits and a post-credits gag | 8:08–9:56 | found | 592.5 | 9:46–9:56 |

On the timeline the events are drawn end to end, so the fifth ends at 3:05 where the sixth begins and the last starts at 8:05. The lane's shares (`.e1` to `.e10` in `style.css`, and `shares` in `page.js`) are those lengths in seconds.

**The quotes** are whole sentences from the answer Claude gave in run B on 0.5.4, attempt 2, at the timestamps that answer gives. They are in each event's list item in `index.html`. The answer itself is kept with the run's records (the session's stream-json), not in this repository; MEDIA.md describes it and quotes parts. The credit, `(qwen3.8-omni-flash, $0.0162)`, is the last line of that answer. The two notes (the butterflies never said to die, so 23 scored strictly; the chinchilla called a mouse) are from MEDIA.md's "Score" paragraph. The four labels on the sound lane (`loud snoring`, `playful pizzicato music`, `loud bang`, `Soft wind ambience`) are phrases from the same answer, placed at 0:36, 2:05, 5:48 and the end, inside the time ranges the answer gives them.

**The sweep's score.** The final numbers are the measured ones. On the way from 15 to 25 the script adds what p0ssess recovered as the playhead passes it: two sound cues that Claude alone could only guess at (birdsong and sound effects, one point each, at 0:06 and 0:20, where the answer first names them) and the four missed events (two points each, at the start of each window). `gains` in `page.js` holds these.

**The stills in "Claude alone".** MEDIA.md says run A made one contact sheet with a frame every 30 seconds. The page draws 20 slits at 0:00, 0:30 and so on. The run's exact offsets were not recorded.

**The images** are made from the film the test ran on: *Big Buck Bunny*, © Blender Foundation, CC BY 3.0, peach.blender.org (the official 720p file, remuxed as MEDIA.md describes, 596.46 s). The footer carries the credit; keep it while the images are on the page.

```sh
# 40 frames, one every 15 s from 7.5 s, then one poster per event (17 48 78 120 176 204 347 448 s;
# two events use frames 17 and 39 of the first 40). Each is 96x54.
ffmpeg -ss $T -i bbb-10min.mp4 -frames:v 1 -vf "scale=96:54:flags=area" $N.png
magick montage *.png -tile 8x6 -geometry 96x54+0+0 sprite.png
magick sprite.png -dither None -colors 128 PNG8:sprite-128.png
magick sprite-128.png -define webp:lossless=true -define webp:method=6 film-frames.webp
# The still for scripts off: the frame at 448 s.
magick 448.png -dither None -colors 64 PNG8:film-still.png
# The strip: 120 slices, one every 5 s, each the middle 12 px of a 100x56 frame.
ffmpeg -i bbb-10min.mp4 -vf "fps=1/5,scale=100:56:flags=area,crop=12:56:44:0,tile=120x1" -frames:v 1 strip.png
magick strip.png -quality 78 -define webp:method=6 film-strip.webp
```

The frames are small on purpose and drawn with `image-rendering: pixelated`, on the same square grid as the ghost. `page.js` reads the sprite as 8 columns of 96×54: frames 0 to 39 for scrubbing and the sweep, 40 to 47 as posters. An event's `data-f` is its poster and `data-at` the second it stands for.

**The sound lane** is the film's own loudness: the audio track as 8 kHz mono, RMS per 5 seconds (120 bars). A bar's height is `(dB + 52) / 35.5`, clamped to 0.06 to 1, of nine tenths of the lane. The path is in `index.html` (`#wave-d`).

```sh
ffmpeg -i bbb-10min.mp4 -vn -ac 1 -ar 8000 -f s16le audio.raw
```

The loud stretches match the ones MEDIA.md measured (4:10 to 4:55, 6:40 to 7:20, 8:10 to 8:50).

**How the widget is built.** `--p` is how much of the film has been watched through p0ssess (0 to 100) and `--h` is where the playhead stands; both are registered custom properties. Each lane has a dim layer and a lit layer clipped to `--p`. The two-way switch is a pair of radio buttons, so with scripts off it still changes the lanes, the frame and the numbers through `:has()`. The script adds the sweep, the event tabs (a tablist for one panel, with arrow keys, Home and End), scrubbing with the pointer, and Previous and Next buttons for touch, where the shortest events are too narrow to tap.

## Every other figure, and where it comes from

Measured calls are from the live results in `../docs/MEDIA.md`:

| On the page | Source |
|---|---|
| $0.0067, a ten-second clip on gemini-3.8-flash, described correctly including its tone | "Describe video", $0.006684 |
| $0.0002, 16 seconds of speech on whisper-large-v3-turbo, word for word with timestamps | "Transcript with timestamps", $0.000180, 16.2 s, word-perfect, 5 timed segments |
| $0.0070, a 1024-pixel image on recraft-v4.1-flash that matched its prompt | "Generate image (image API)", $0.007000, 1024×1024, on prompt |
| The ghost-with-a-lantern image in the images cell (`ghost-lantern.webp`) | A real p0ssess 0.5.4 run on 2026-11-11 through the p0ssess agent: the cell's prompt word for word, recraft/recraft-v4.1-flash, no resolution set, $0.0070. It returned an 832×1280 WebP; the page shows rows 150 to 774, saved at quality 80. |
| "a few cents more" for the hand-off itself | $0.04 to $0.12 of Sonnet usage, `../docs/RESEARCH.md` |
| $0.0938, $0.0040, $0.0162 of the film run's $0.1140 | MEDIA.md, the run B on 0.5.4 table: Sonnet, Haiku, OpenRouter |
| 28,883 tokens against 28,555 | same table, "Main context at the last call" |

The three things to say to Claude under the abilities, "Use deepseek flash to draft tests for stats.py" and the three rules are from `../README.md`. The status line's three states are from `../docs/ENGINE.md`. The list of what it won't do follows `../README.md` (Privacy, Respects a budget, Rates stakes, Learns quietly) and `../docs/DESIGN.md`.

Engine figures are estimates and the page labels each as one. They are output of the 0.5.4 release binary (its SHA-256 matches `../dist/SHA256SUMS`) in a scratch `P0SSESS_HOME` with no history and no network, so prices are the shipped snapshot's:

```sh
p0ssess engine set plan api
p0ssess engine recommend --use-case pdf-table-extract --top 200 --json   # the engine chart
p0ssess engine recommend --use-case test-generation --top 200 --json     # $0.0081, rank 1's cash
p0ssess engine recommend --use-case code-review-diff --top 200 --json    # $0.0021, rank 1's cash
p0ssess engine usecases | wc -l                                          # 104
```

In a chart a score is 100 × (1 − `p_fail_upper`), a total is the option's `total_usd`, and "call" is `breakdown.cash`, rounded. The engine chart shows ranks 1, 2, 5, 7 and 10 of 12. The gates are 100 × (1 − the ceiling) for each level of stakes, and "a mistake costs" is the harm, both from ENGINE.md's defaults.

The usage chart is `--use-case long-agent-session` on a subscription plan after one reading fed through `p0ssess statusline`:

| Reading | 5-hour window | Weekly window | The engine says |
|---|---|---|---|
| Early | 5% used, resets in 12,600 s | 2% used, resets in 302,400 s | projected 17%, λ 0.17: Claude $0.3461, GPT-6.1 Sol $0.8927 |
| Late | 78% used, resets in 2,225 s | 40% used, resets in 302,400 s | projected 89%, λ 0.82: GPT-6.1 Sol $1.2439, Claude $1.4670 |

Both charts share one scale, the late Claude total.

The first-run summary in the install section is `p0ssess me --brief` from 0.5.4 in a fresh scratch home with an empty Claude config, with the home path and the page address shortened.

## How the charts are drawn

Each chart is an `ol.bars` in a `figure.chart`. Rows are sorted by total and the winner is first, with a visible "lowest total" tag, so the result does not depend on color. A row holds the name, the total, the bar, and under it the score against the gate and the price of the call alone. A chart shows only some of the engine's ranked options, and its caption says how many.

Each bar is an SVG `rect` whose `width` attribute is a percentage, since the CSP forbids inline styles: the total as a share of the largest total in that chart, from a zero baseline. With motion allowed, bars grow at about one speed, so the shortest finishes first and then gets its tag.

The gate table draws the 0 to 100 score as the rule under each row; the classes `g20`, `g35`, `g60` and `g80` place the violet part, which is the scores that clear that gate. The bar under "Where the film run's $0.1140 went" is three `rect`s whose widths are each part's share.

## The mark

`ghost.svg` is a copy of `../assets/ghost.svg`. It is the tab icon, the lockup, the island's home link, the footer mark, the playhead's rider and both ghosts in the hand-off, so replacing that one file changes all of them. It is drawn on a 16-unit grid, like the wordmark's pixels, and every use is a whole multiple of the unit (`--u`) so it stays crisp. The blink covers the top of the two slit eyes with the mask's color, at columns 6 and 9, rows 5 and 6 (`.g .lid` and `--ghost-mask` in `style.css`). Move those if the eyes move. The ghost blinks when it starts the sweep and when it lands, never on a loop.

The three icons over the abilities are drawn on the same grid in the ghost's four colors.

## The hand-off

`figure.relay` is three stops on one line: Claude, the possessed Haiku, and another model. The timeline is CSS, 4.8 seconds, with every part on the same clock; the comment above the keyframes in `style.css` gives it. It ends on the still frame, with the answer's credit beside Claude. `page.js` measures where the riding ghost lands (the seat in the Haiku pill) and names the next job on each Replay.

The jobs are in `data-jobs` on the figure (model, sentence, cost), and the same four are in its screen-reader caption; change both together. Each is a measured call from the table above.

## Going live

`release.js` is the one place the page reads addresses from: `repo` (the repository's page on the git server, without a trailing slash), `docs` (the documentation site) and `version` (the published release, without the `v`). With `repo` and `version` set, the four download buttons, `SHA256SUMS` and "Release notes" point at `<repo>/releases/download/v<version>/<file>` and `<repo>/releases/tag/v<version>`, which is where Gitea serves a release, and the install command shows `<repo>.git`. With either one empty the download controls stay disabled and no outside link is shown. With `docs` empty the Docs link and the two links into the documentation (`data-doc`, a path under `docs`) are hidden.

For each release:

1. Publish it (see `../docs/RELEASE.md`), then set `version` to it. Set it only once the files are up, since the buttons link straight to them. The files are `p0ssess-linux-amd64`, `p0ssess-linux-arm64`, `p0ssess-darwin-arm64`, `p0ssess-<version>.tar.gz` and `SHA256SUMS`.
2. Optionally paste that release's three sums into `sha256` (from its `SHA256SUMS`, which is `../dist/SHA256SUMS` at the tag) to print each under its file. Never type one from memory.

The site's own address, https://p0ssess.me, is only in `index.html`: the canonical link, `og:url` and `og:image`. `og.png` is the share image for link previews: the top of the hero at 1200×630, taken with reduced motion at a device scale of 1, with the parts below the frame hidden so nothing is cut mid-line. Retake it when the hero changes (`shoot.js --og`), and keep `og:title` and `og:image:alt` in step.

With scripts off the buttons stay disabled, because the addresses live in the script.

## Screenshots

`shoot.js` serves the folder on 127.0.0.1, blocks every other request, and captures 1440×900 and 390×844 with reduced motion, so the captures are deterministic: the top, the film test, each section and the end. `--only name,name` retakes single shots and `--full` adds full-page captures. `--live` adds captures with motion allowed (`live-*.png`): the sweep at three moments on each size, the hand-off part-way through, and the island over the engine section. `--og FILE` writes the share image. It needs Node, `playwright-core` and a Chromium-based browser:

```sh
node site/shoot.js --browser /path/to/chromium --out site/shots --full --live
node site/shoot.js --browser /path/to/chromium --only none --og site/og.png
```

Without an installed `playwright-core`, set `PLAYWRIGHT_CORE` to a copy of it; Playwright for Python ships one as `playwright/driver/package`, next to its own `node`.

It prints console errors, failed and outside requests, and any horizontal overflow. The PNGs in `shots/` were reduced to 256-color palettes afterwards (`magick in.png -colors 256 PNG8:out.png`) to keep them small.

## Deploy

Copy `index.html`, `style.css`, `page.js`, `release.js`, `ghost.svg`, `film-frames.webp`, `film-strip.webp`, `film-still.png`, `og.png` and `fonts/` to any static host. `scenes.js` is gone; remove it from the host. `README.md`, `shoot.js`, `shots/` and `docs-build/` are not needed there.

The page carries a strict Content Security Policy in a `<meta>` tag. If the host can set headers, send the same policy as a header and add what a meta tag cannot express:

```
Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'
Referrer-Policy: no-referrer
X-Content-Type-Options: nosniff
```

Serve `.woff2` as `font/woff2` and `.webp` as `image/webp`, and cache `fonts/` and the film images for a long time. Outside addresses are only followed when someone clicks a link; the page itself loads nothing from them. Uncompressed, the page and everything it loads come to about 280 KB.

## Fonts

Obel Mono (headings, numbers, prompts; instanced to weights 400–700 and subset), IBM Plex Sans 400 (body), and the single glyph ᗣ from Noto Sans Canadian Aboriginal for the status-line mark. All are under the SIL Open Font License 1.1; the license texts are in `fonts/`.
