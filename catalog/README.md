# Use-case catalog

`usecases.yaml` lists 104 distinct jobs Claude Code could hand to another model, or should keep. Each entry is a starting prior for the Predictive Model Engine (PME), not a fixed rule: the engine still rates each real task's stakes and cost from live signals and the user's history.

- **87 first-pass entries** come from OpenRouter usage data and from Claude Code gaps and delegation tools.
- **17 stakes-review entries** (`origin: stakes-review`, n 88–104) were added when the catalog was reviewed through the stakes dimension in [DESIGN.md](../docs/DESIGN.md).

The research behind it is in [docs/RESEARCH.md](../docs/RESEARCH.md). Model facts and benchmark scores for every model named here are in [registry/](../registry/).

Validate with `python3 catalog/validate.py` (needs PyYAML and jsonschema). It checks the file against `schema.json`, and checks that ids are unique, `n` runs 1–104, `related` ids exist, and every model id is in `registry/models.json`.

## Fields

| Field | Meaning |
|---|---|
| `n` | Position, 1–104. Matches the numbering in RESEARCH.md. |
| `id` | Stable kebab-case id. Never reused or renamed. |
| `origin` | `first-pass` or `stakes-review`. |
| `task`, `inputs`, `outputs` | What the job is and what goes in and out. |
| `stakes` | 1 trivial, 2 routine, 3 important, 4 critical (DESIGN.md's rubric), or `inherit` when the job takes the level of the work it carries (only `limit-fallback-coding`). For a range, `stakes` is the first-listed level. |
| `stakes_range` | `[low, high]` when the job covers more than one kind of work, for example SQL queries (2) versus production schema changes (4). The engine rates the instance. |
| `stakes_reason` | One line on why. |
| `quality_bar` | What an accepted result must satisfy. |
| `path` | `direct` (one model call, or a short chain of calls), `agent` (a sandboxed OpenCode session), `keep_on_claude` (Claude does it, in the session or with a native Claude subagent), or `local_tool` (a tool with no model, or a local step that beats a model). |
| `local_tool` | The tool, when `path` is `local_tool`. |
| `engine` | An OpenRouter PDF engine that does the work without a chat model (`pdf-table-extract`). |
| `recommended` | OpenRouter model id of the first choice. For `keep_on_claude` it names the Claude tier; Sonnet 5.5 stands in for "the session's own lead model" where the research says only "Claude (stay)". `null` only when a local tool or engine does the work. |
| `ensemble` | Further models run alongside `recommended` (two-family reviews, consensus polls). |
| `pipeline` | Ordered steps when the job chains tools and models; `recommended` is the step the alternatives replace. |
| `alternatives` | 1–3 OpenRouter model ids on equal footing with `recommended`, Claude models included where they fit. Fewer than two only when the evidence names fewer. |
| `other_options` | Non-model alternatives (a local script, a free PDF parser). |
| `est_cost_usd` | `{min, max}` per run for `recommended`, in US dollars at list price, before OpenRouter's 5.5% credit fee. A bound is `null` when the source gives only one side; the whole field is `null` when no price is exposed. For `keep_on_claude` it is the Claude list-price equivalent, which subscribers pay in usage, not cash. |
| `cost_assumptions` | How the number was reached: a size profile, a session-cost median, a measured eval cost, or a media formula. |
| `budget_sensitivity` | `low`: far below the $0.02 auto-approve. `medium`: near the limit or scales with volume. `high`: typically above $0.10 or open-ended. |
| `verify` | `level` plus `method`. `full`: Claude reviews all of it (a diff from a worktree, every finding). `spot`: Claude spot-checks claims it can verify. `mechanical`: an objective check decides acceptance (tests, schema, recomputation, placeholder parity, render). `none`: passed through, labeled with the model, or internal. |
| `evidence` | List of `{source, url or dataset, as_of, detail}`. `as_of` is the data's own date for datasets and the access date for web pages, unless the page carries a publication date. |
| `stakes_routing_change` | Present on the 13 first-pass entries whose routing changed once stakes were applied. |
| `related` | Ids of entries that take over part of this one's work. |
| `notes` | Honest caveats: unconfirmed price units, vendor-only evidence, no clean benchmark, no ZDR endpoint, single provider. |

## Size profiles and shorthand

Token jobs use five size profiles, which are assumptions, not measurements: **T** 1k in / 50 out, **S** 5k / 1k, **M** 30k / 3k, **R** 30k / 6k (review with reasoning, billed as output), **L** 200k / 4k.

In `cost_assumptions`, **SC** means OpenRouter's session-cost dataset (median dollars per Claude Code session by turn range, as of 2026-11-02T07:25:14.332Z) and **OE** means OpenRouter's own evals (measured cost per task, as of 2026-11-09T00:00:58Z). Both are CC BY 4.0; their citation lines are in [registry/ATTRIBUTION.md](../registry/ATTRIBUTION.md).

## Counts

| Path | Entries |
|---|---|
| direct | 73 |
| agent | 16 |
| keep_on_claude | 10 |
| local_tool | 5 |

| Stakes (first-listed level) | All 104 | First pass (87) | Stakes review (17) |
|---|---|---|---|
| 1 | 40 | 37 | 3 |
| 2 | 44 | 36 | 8 |
| 3 | 12 | 10 | 2 |
| 4 | 7 | 3 | 4 |
| inherit | 1 | 1 | 0 |

Thirteen first-pass entries carry a stakes routing change: 2, 3, 4, 8, 12, 14, 46, 48, 56, 58, 60, 61, 64.
