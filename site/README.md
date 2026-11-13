# p0ssess website

The site is static: `index.html`, `style.css`, `page.js`, `release.js`, `ghost.svg`, two images (`film-strip.webp`, `ghost-lantern.webp`), `og.png` (the share image) and `fonts/`. No build step, no framework, and no requests to anything but its own files.

Beside the home page are six pages, each an `index.html` in its own folder: `pricing/`, `business/`, `data/`, `terms/`, `privacy/` and `refunds/`. They load `style.css`, then `pages.css` and `pages.js`.

The home page is an advertisement, and its subject is cost. In order:

1. **The hero.** The headline, what p0ssess is, the two paid plans, and beside them one rule and one job: what a run of it costs with the rule, against three ways of running Claude without it, with every measured run on a line of dollars. A three-way switch picks which Claude setup the saving is measured against.
2. **The proof.** The bar a comparison has to clear, and a second job that cleared it, on the hero's scale.
3. **How it works.** The hand-off drawn as three stops, and where one run's money went.
4. **For a team.** One rule from the owner reaching every seat.
5. **New senses.** What Claude can't do alone: a film watched, a recording heard, an image made. It is here, and it is second.
6. **Your limits.** Budgets, rules, the sandbox and what it won't do.
7. **Pricing**, then **Install**.

A pill dock (the island) is fixed to the top once the page scrolls. It holds a dot for each part, the name of the one in view, and the way to Pro.

## The rule this site keeps

Every number on these pages is a measured run, and each one is listed below with its source. Nothing is estimated, rounded up or invented, and a result that a skeptic could wave away is not shown. When a figure changes, change it here in the same commit.

The pages must keep saying these things, because they are true:

- The benchmark's datasets were built for it, a script graded them, and Claude is priced at API list price. The note under the hero's chart says so and links to the method.
- Most of the jobs tested did not clear the bar. The home page says so and the business page shows every one of them.
- The savings runs used the free plugin. The pricing page says so.
- On the film, p0ssess cost more in total and took longer. Only the cost for each point scored is lower. Do not write that it was cheaper.

## Preview locally

Browsers refuse web fonts over `file://`, so serve the folder:

```sh
python3 -m http.server --bind 127.0.0.1 --directory site 8000
```

Then open <http://127.0.0.1:8000/>, or a page beside it such as <http://127.0.0.1:8000/pricing/>. The site is dark only. The hero is two columns from 1024 px, sections split in two from 860 px, and narrower screens stack everything.

## Motion

One motion language: arrivals ease out (`--out`), travel that stays on screen eases in and out (`--io`), and the spring (`--spring`) is kept for the island, the two-way switches and a press. Nothing fades up as it scrolls in. What moves:

- **The ride**, the page's one authored moment. With motion allowed the hero opens with the ghost where Claude stands and, once the line of dollars is on screen, it rides to p0ssess's mean run in 1.5 seconds. The line it leaves behind is the saving, and the figure counts down beside it. Choosing another view rides again, between the two views.
- **Hairlines** draw out of their 24 px violet start when they scroll into view, a chart's **marks** land, and the bars of a side-by-side grow from the baseline, each once.
- **The hand-off** plays once when it scrolls into view (4.8 seconds) and again on Replay.
- **The island** slides its violet dot to the part in view and resizes to its name.

Every animated part has a complete still state in the markup. With `prefers-reduced-motion: reduce`, or with scripts off, the page shows that: the ghost at p0ssess's mean, every run on its lane, the hand-off possessed with one measured job named, marks and bars in place. With scripts off the switch still changes the figures, lights the lanes and moves the ghost, through `:has()`. If scripting is on but `page.js` never runs, the hero's proof shows itself after 2.5 seconds.

## The savings benchmark

The cost figures come from one benchmark, run on November 13, 2026. Its design was written and committed before the first measured run, with the criterion for a win. The business page's "How it was measured" is the public summary. The design, the generators, the graders and one line for every run are kept with the benchmark's records, not in this repository.

In short: each run is one headless Claude Code 2.1.323 session handed a whole batch, from an empty configuration, with a prompt that never mentions models or cost. Arm A is Claude alone. Arm B adds the public p0ssess 0.6.0 plugin and one rule that sends the job's kind of work to one model, which the engine picked before any run. Arm C, the control, is Claude alone told to hand the bulk of the work to its own Haiku subagent. Cost is Claude Code's own figure at API list prices plus what OpenRouter charged. A job is a win when the most expensive p0ssess run costs less for each correct item than the cheapest Claude run, accuracy is within three points on average and five in the worst run, every p0ssess run delegated, and there are at least three runs each way. Only a win that is at least twice as cheap is advertised.

Every benchmark figure on the pages:

<!--gen:readme-figures-->
| On the pages | Figure | From |
|---|---|---|
| Hero: unit tests, Claude Opus 5.5 alone | mean $1.2644 a run, $1.10 to $1.45, 3 runs: $1.0966, $1.4520, $1.2446; 45 of 45 test files pass; mean 322 s | arm A, `unit-tests`, Opus |
| Hero: Claude Opus 5.5 with its own Haiku subagent | mean $0.6362 a run, $0.60 to $0.71, 3 runs: $0.5985, $0.5956, $0.7147; 45 of 45 test files pass; mean 381 s | arm C, `unit-tests`, Opus |
| Hero: with p0ssess, Opus in charge | mean $0.2842 a run, $0.27 to $0.30, 3 runs: $0.2763, $0.2739, $0.3024; 45 of 45 test files pass; mean 366 s | arm B, `unit-tests`, Opus |
| Hero: Claude Sonnet 5.5 alone | mean $0.6968 a run, $0.60 to $0.86, 3 runs: $0.8650, $0.5973, $0.6280; 45 of 45 test files pass; mean 256 s | arm A, `unit-tests`, Sonnet |
| Hero: Claude Sonnet 5.5 with its own Haiku subagent | mean $0.1565 a run, $0.09 to $0.22, 3 runs: $0.0947, $0.2243, $0.1504; 44 of 45 test files pass; mean 247 s | arm C, `unit-tests`, Sonnet |
| Hero: with p0ssess, Sonnet in charge | mean $0.0961 a run, $0.09 to $0.10, 3 runs: $0.0969, $0.0911, $0.1004; 45 of 45 test files pass; mean 196 s | arm B, `unit-tests`, Sonnet |
| Hero, "cheaper": against Opus with its Haiku, Opus alone, Sonnet alone | 2.24, 4.45 and 7.25 times | mean cost per correct item of the Claude setup over p0ssess's, same lead model |
| The hero's positions (`--p-opus` and the rest, in `style.css`) | --p-opus: 18.95; --p-sonnet: 6.41; --a-opus: 84.29; --c-opus: 42.42; --a-sonnet: 46.45; | each setup's mean as a share of the axis, $0 to $1.50 |
| "Where one run's $0.2763 went" | Claude Opus 5.5 $0.2438, Claude Haiku 5.5 $0.0034, deepseek-v4.1-flash $0.0291 | arm B run 1: Claude Code's cost per model, and the OpenRouter ledger |
| The proof, pricing: ticket triage, 300 tickets | alone mean $0.7332, $0.64 to $0.90; with p0ssess mean $0.3266, $0.24 to $0.39; 3 and 3 runs; all 300 right in each; 2.24 times cheaper | arms A and B, `ticket-triage` N=300, Opus |
| The hand-off figure's two jobs on deepseek-v4.1-flash | $0.0291 for the tests of 15 modules, $0.0781 for 300 tickets | the OpenRouter part of arm B run 1 of each job |
| Pricing, "less a run" | $0.98 and $0.41 | the difference of the two means, Opus in charge; the page says it is the mean |
<!--/gen:readme-figures-->

The business page's two charts and their tables hold every job that was tested, with Claude Opus 5.5 and with Claude Sonnet 5.5 in charge. A mark's position there is that run's cost for each correct item divided by the mean of Claude alone on the same job, so 1× is Claude's mean. The verdict beside each row is the criterion above applied to those runs. A run with nothing right has no cost for each right answer and is drawn at the right edge.

The hero opens on the comparison with Claude's own Haiku subagent, because it answers the first thing a skeptic asks. The rule in the hero is the line the p0ssess runs carried, word for word. In 0.6.0 a rule limited which model could be used but did not make Claude delegate, so arm B carried it in words as well. The business page says so.

## The film test

One prompt, run twice on Claude Sonnet 5.5: "Describe what happens in bbb-10min.mp4 with timestamps, including what you can tell about the sound." The film is *Big Buck Bunny*, 9:56 long. Run A was Claude alone; run B added p0ssess 0.5.4, which sent the film to qwen3.8-omni-flash. One run each, scored by hand against a checklist written before either run: two points an event, one a sound cue. The source is `../docs/MEDIA.md` ("Ten minutes of video: Claude alone and with p0ssess", and "Run B on 0.5.4", attempt 2).

| On the pages | Claude alone | With p0ssess |
|---|---|---|
| Score | 15 of 25 | 25 of 25 |
| Events found, of ten | 6 (it missed the fifth, sixth, eighth and ninth) | 10 |
| Total cost | $0.0999 | $0.1140 |
| Wall time | 40 s | 262 s |
| What watched | 20 stills, one every 30 s, and loudness readings | the whole film at 1 frame a second, with sound |
| Cost for each point (derived) | $0.0999 / 15 = $0.0067 | $0.1140 / 25 = $0.0046 |

The ten events and their windows are the checklist's. In the figure they are drawn end to end, and the shares (`.e1` to `.e10` in `style.css`) are their lengths in seconds. "Scored strictly, one event is a close call and the total would be 23" is MEDIA.md's own note: the answer never says the butterflies die.

The strip is made from the film the test ran on: *Big Buck Bunny*, © Blender Foundation, CC BY 3.0, peach.blender.org. The footer carries the credit; keep it while the strip is on a page.

```sh
# 120 slices, one every 5 s, each the middle 12 px of a 100x56 frame.
ffmpeg -i bbb-10min.mp4 -vf "fps=1/5,scale=100:56:flags=area,crop=12:56:44:0,tile=120x1" -frames:v 1 strip.png
magick strip.png -quality 78 -define webp:method=6 film-strip.webp
```

"Claude alone" shows the same strip dimmed, with 20 slits lit at 0:00, 0:30 and so on (`.s-slits`). The run's exact offsets were not recorded.

## The other figures

| On the pages | Source |
|---|---|
| The speech recording. Claude alone: about 7 min, $0.0845, transcript wrong. With p0ssess: 8 s, $0.0519, transcript correct. | p0ssess benchmark run, 2026-11-11. The same speech recording was given to Claude Sonnet 5.5 twice. Alone, Claude installed faster-whisper (base.en) and ran it; with p0ssess the file went to whisper-large-v3-turbo. The page does not name the recording or quote either transcript. The bar under "8 s" is 8 of 420 seconds, 1.9%. |
| The image of a ghost with a lantern (`ghost-lantern.webp`), $0.0070 on recraft-v4.1-flash | A real p0ssess 0.5.4 run on 2026-11-11 through the p0ssess agent: the prompt on the page word for word, recraft/recraft-v4.1-flash, no resolution set, $0.0070. It returned an 832×1280 WebP; the page shows rows 150 to 774, saved at quality 80. |
| In the hand-off figure: $0.0162, a ten-minute film on qwen3.8-omni-flash | The film test's run B, the other model's part of $0.1140. |
| In the hand-off figure: $0.0070, a 1024-pixel image on recraft-v4.1-flash | `../docs/MEDIA.md`, "Generate image (image API)", $0.007000, 1024×1024. |

The rules in "Your limits" and the status line segment are from `../README.md` and `../docs/ENGINE.md`. What it won't do follows `../README.md` (Privacy, Respects a budget, Rates stakes) and `../docs/DESIGN.md`. The Team command is the client's own (`p0ssess team rule add`), and what Team includes follows the terms of service.

## Pricing, business and policy pages

Six pages sit beside the home page. They share the top bar (the lockup links home; Pricing, For business, Docs, Source and Get Pro, with the current page marked `aria-current="page"`) and the footer. The footer's row of links (Pricing, For business, Terms, Privacy, Data, Refunds) is on the home page too, and below 760 px, where the top bar keeps only Get Pro, it is the way between pages.

| Page | What it holds |
|---|---|
| `pricing/` | Three tiers, Free, Pro and Team, with what each includes, then what one rule is worth and how billing works. The buy buttons follow the offer (below). |
| `business/` | The cost case for people who pay for Claude at work: every job tested, with every run; how it was measured; two side-by-sides on what Claude can't do alone; what Pro measures on your own work; and Team. |
| `data/`, `terms/`, `privacy/`, `refunds/` | The full text of the policy, with its effective date and a table of contents. Each is the Markdown source of that policy, set in this page's HTML without rewording. |

With `prefers-reduced-motion: reduce` or with scripts off every page is complete: hairlines, marks and bars are drawn in full, the prices are in the markup and the buy buttons read "Opening soon, get notified".

### The offer

What is for sale is configured in one place, `offer` in `release.js`:

- `api` is the cloud API's address without a trailing slash, or `""` to leave the page on its own.
- `prices` holds `pro_month`, `pro_year` and `team_seat_month` in US cents, for when the API cannot be reached. `null` leaves the price that is in the markup.

The prices are also written in the HTML ($4 a month, $36 a year, $8 a seat), on the home page, in the top of the pricing page and in its tiers, so they read with scripts off. When a price changes, change them there, in `release.js` and on the server.

| `offer.api` | What the API says | Prices | Pro's and Team's button |
|---|---|---|---|
| `""` | not asked | the markup, or `offer.prices` | "Opening soon, get notified", a `mailto:` link to support@p0ssess.me |
| set | no answer within 4 seconds, or an error | the markup, or `offer.prices` | the same `mailto:` link |
| set | `checkout: "opening_soon"` | from `plans` | "Opening soon, get notified", which opens the address form |
| set | `checkout: "open"`, a Pro plan `available`, and an https `start_url` | from `plans` | Pro: "Get Pro", a link to `start_url`. Team: the address form |

`pages.js` asks `GET <api>/v1/pro/offer` and reads each plan:

- `placeholder: false` shows `price.amount_cents` as dollars, if `price.currency` is `usd`. Under Pro's yearly price it also shows that amount by the month.
- `placeholder: true` (or anything but `false`) means the price is not decided. The page hides the number and says the price is being set. It never shows an undecided amount.
- `sandbox_hours_per_month` on a Pro plan is shown as the allowance for the billing period in view.
- `available` on a Pro plan allows "Get Pro" once checkout is open.
- `recommended` picks which billing period the Yearly and Monthly switch starts on, unless the reader has already chosen. The page ships on Yearly.

The address form is used only once the API has answered with an offer. It sends `POST <api>/v1/waitlist` with the body `email=...&plan=pro|team&source=site` as `application/x-www-form-urlencoded`. That content type keeps it a simple request, so the browser sends no preflight. The API has to answer both routes with an `Access-Control-Allow-Origin` header that allows the site, or the page cannot read the answer. What the reader sees, in a live region under the form:

| Answer | Message |
|---|---|
| 204 | "You’re on the list. We’ll send one email when Pro opens, then delete your address." (or "Team") |
| 400, or an address the browser already rejects | "That doesn’t look like an email address." |
| 429 | "Too many tries. Try again in a while." |
| anything else, or no answer in 10 seconds | "Couldn’t reach the server. Try again later." |

The send button is disabled while a request is out. Sending the same address twice is not an error. The form is in the markup with `hidden` and no `action`, and the policy has `form-action 'none'`, so with scripts off there is no form to submit.

**The policy and the address change together.** Every page forbids connections (`default-src 'none'`). The pricing page alone adds `connect-src https://cloud.p0ssess.me` to the policy in its `<meta>` tag. When `offer.api` is set, the same origin has to be in that `connect-src`, and if the host also sends the policy as a header, in the header for `/pricing/` too, since a browser enforces both.

### The reading layout

The policies use one layout: a heading block (`.doc-head`), then `.doc`, which holds a table of contents (`nav.toc`) and the text (`.prose`, 68 characters wide). From 960 px the contents sit beside the text and stay in view; on narrower screens they come first. In `.prose`, an `h2` with the class `hl` starts a part and an `h3` a point within it; `strong` is a run-in heading; `ol.vows` is the data policy's numbered list of commitments; `pre.sample` is a sample record; `div.tbl` is a table that becomes one card to a row on a phone.

The text is the policy's Markdown, set mechanically: headings get an id from their words, a link to another policy's Markdown file becomes a link to that policy's page, and nothing is reworded. When a policy changes, set the page again from the Markdown rather than editing the HTML by hand, and keep the effective date line.

## How the charts are drawn

A chart is an inline SVG with no `viewBox`, so its text keeps its size and every horizontal position is a percentage of the chart's width: the CSP forbids inline styles, and attributes are not styles. The axis starts at zero.

Every run is one mark. A violet square is a run with p0ssess, a gray circle is Claude alone and a gray ring is Claude with its own Haiku subagent, so the three are told apart by shape as well as color. Runs that land on the same cent are stepped apart, and each mark carries a 2 px ring of the ground. The band behind a group is its range, lowest run to highest. Each group has its name and its range in words beside it, and the hero's and the business page's charts have a table of the same runs under them, so nothing is readable only from a mark. With a pointer on a mark, `page.js` and `pages.js` show that run's figures beside it (`data-tip`).

The hero's chart is five lanes on one axis, a lane for each setup (`ol.lanes`). The lanes of the view in hand are lit and the others step back. Under them, as HTML, are the ghost (`.head`), which stands on the axis at p0ssess's mean, the saving (`.span`), drawn from the ghost to the mean of the Claude setup in view, and the saving in words above it (`.span-say`). `--at` is the ghost's position and `--to` the other end, as percentages of the axis. Both are registered custom properties, so the ride between two views can be transitioned. The five means are in `.hero` in `style.css` (`--p-opus` and the rest).

The marks, the legends' ranges and the tables of runs sit between `<!--gen:NAME-->` comments in the HTML, as does the table of figures above. A script kept with the benchmark writes them from its run records, so the charts and the numbers cannot drift apart. To change one by hand, change it between its comments and in the table above.

Each side-by-side under "new senses" is a table (`table.duel-t`) whose first measure is also drawn as a bar, both rows on one scale from zero. The bar under "Where one run's money went" is three `rect`s whose widths are each part's share.

## The mark

`ghost.svg` is a copy of `../assets/ghost.svg`. It is the tab icon, the lockup, the island's home link, the footer mark, the rider on the hero's line, both ghosts in the hand-off and the seats in the Team figure, so replacing that one file changes all of them. It is drawn on a 16-unit grid, like the wordmark's pixels, and every use is a whole multiple of the unit (`--u`) so it stays crisp. The blink covers the top of the two slit eyes with the mask's color, at columns 6 and 9, rows 5 and 6 (`.g .lid` and `--ghost-mask` in `style.css`). Move those if the eyes move. The ghost blinks when it starts a ride and when it lands, never on a loop.

## The hand-off

`figure.relay` is three stops on one line: Claude, the possessed Haiku, and another model. The timeline is CSS, 4.8 seconds, with every part on the same clock; the comment above the keyframes in `style.css` gives it. It ends on the still frame, with the answer's credit beside Claude. `page.js` measures where the riding ghost lands (the seat in the Haiku pill) and names the next job on each Replay.

The jobs are in `data-jobs` on the figure (model, sentence, credit), and the same four are in its screen-reader caption; change both together. Each is a measured call from the tables above.

## Going live

`release.js` is the one place the pages read addresses from: `repo` (the repository's page on the git server, without a trailing slash), `docs` (the documentation site), `version` (the published release, without the `v`) and `offer` (above). With `repo` and `version` set, the download buttons, `SHA256SUMS` and "Release notes" point at `<repo>/releases/download/v<version>/<file>` and `<repo>/releases/tag/v<version>`, and the install command shows `<repo>.git`. With either one empty the download controls stay disabled and no outside link is shown. With `docs` empty the Docs link is hidden.

For each release:

1. Publish it (see `../docs/RELEASE.md`), then set `version` to it. Set it only once the files are up, since the buttons link straight to them. The files are `p0ssess-linux-amd64`, `p0ssess-linux-arm64`, `p0ssess-darwin-arm64`, `p0ssess-windows-amd64.exe`, `p0ssess-<version>.tar.gz` and `SHA256SUMS`.
2. Optionally paste that release's sums into `sha256` (from its `SHA256SUMS`, which is `../dist/SHA256SUMS` at the tag) to print each under its file. Never type one from memory.

The site's own address, https://p0ssess.me, is in each page's canonical link and `og:url`, and in the home page's `og:image`. `og.png` is the share image for link previews: the top of the hero at 1200×630, taken with reduced motion at a device scale of 1, with the small print under the chart hidden so nothing is cut mid-line. Retake it when the hero changes (`shoot.js --og`), and keep `og:description` and `og:image:alt` in step.

With scripts off the download buttons stay disabled, because the addresses live in the script.

## Screenshots

`shoot.js` serves the folder on 127.0.0.1, blocks every other request, and captures 1440×900 and 390×844 with reduced motion, so the captures are deterministic: the top, the hero's line, each section and the end. `--only name,name` retakes single shots and `--full` adds full-page captures. `--pages` adds the six pages beside the home page (below). `--live` adds captures with motion allowed (`live-*.png`): the hero's ride at three moments on each size, the hand-off part-way through, and the island over a later section. `--og FILE` writes the share image. It needs Node, `playwright-core` and a Chromium-based browser:

```sh
node site/shoot.js --browser /path/to/chromium --out site/shots --full --live
node site/shoot.js --browser /path/to/chromium --only none --og site/og.png
```

Without an installed `playwright-core`, set `PLAYWRIGHT_CORE` to a copy of it; Playwright for Python ships one as `playwright/driver/package`, next to its own `node`.

`--pages` captures each of the six pages whole at both sizes (`desk-pricing.png`, `phone-data.png` and so on), then the pricing page against a cloud API that `shoot.js` answers itself. For every capture it serves `release.js` with `offer.api` set to `""`, or to `https://cloud.p0ssess.me` when the API is mocked, whatever the file says, and fulfills requests to that address in the browser, so nothing leaves the machine:

| Capture | State |
|---|---|
| `*-pricing.png` | No API: the prices in the markup, "Opening soon". |
| `*-pricing-mock-soon.png` | The API answers `opening_soon` with plans whose prices are not decided. |
| `*-pricing-mock-waitlist-bad.png` | The address form after an address that is refused. |
| `*-pricing-mock-waitlist-ok.png` | Both forms after an address that is accepted. |
| `*-pricing-mock-open.png` | The API answers `open` with decided plans. |

The amounts in the mock (`TEST` in `shoot.js`) are test values, not prices. The run fails a check if one shows while its plan is not decided. The `-mock-open` captures show them, under a band across the top that says so. Do not publish those captures as if they were prices. `--only` takes page names too, so `--pages --only pricing` retakes the pricing captures alone.

It prints console errors, failed and outside requests, failed checks and any horizontal overflow. The PNGs in `shots/` were reduced to 256-color palettes afterwards (`magick in.png -colors 256 PNG8:out.png`) to keep them small. `shots/` holds the home page only.

## Deploy

Copy `index.html`, `style.css`, `pages.css`, `page.js`, `pages.js`, `release.js`, `ghost.svg`, `film-strip.webp`, `ghost-lantern.webp`, `og.png`, `fonts/` and the six page folders (`pricing/`, `business/`, `data/`, `terms/`, `privacy/`, `refunds/`, each with its `index.html`) to any static host. The pages link to each other as folders (`../pricing/`), so the host has to serve a folder's `index.html` for its address; with nginx, `try_files $uri $uri/` and `index index.html` do that. `README.md`, `shoot.js`, `shots/` and `docs-build/` are not needed there. `film-still.png` is no longer on any page; it stays in the folder for a deploy script that copies it by name.

Every page carries a strict Content Security Policy in a `<meta>` tag. If the host can set headers, send the same policy as a header and add what a meta tag cannot express. For `/pricing/` the header also needs the page's `connect-src` (see "The offer"), or the header will block what the meta tag allows:

```
Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'
Referrer-Policy: no-referrer
X-Content-Type-Options: nosniff
```

Serve `.woff2` as `font/woff2` and `.webp` as `image/webp`, and cache `fonts/` and the images for a long time. Outside addresses are only followed when someone clicks a link; the pages load nothing from them, except the pricing page's two calls to the cloud API once `offer.api` is set. Uncompressed, the home page and everything it loads come to about 180 KB.

## Fonts

Obel Mono (headings, numbers, prompts; instanced to weights 400–700 and subset), IBM Plex Sans 400 (body), and the single glyph ᗣ from Noto Sans Canadian Aboriginal for the status-line mark. All are under the SIL Open Font License 1.1; the license texts are in `fonts/`.
