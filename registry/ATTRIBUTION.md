# Attribution and licenses

p0ssess's own code and text are under the repository [LICENSE](../LICENSE). The third-party data in `registry/`, in `catalog/usecases.yaml` and in `docs/RESEARCH.md` keeps its own license, listed here. Changes made to licensed data: records were filtered to the models the catalog references, joined to OpenRouter model ids by normalized name, and reformatted as JSON; a few fields are derived and named as such (`binomial_se_computed`, `cheapest`, `best_data_policy`). No value was altered.

## LMArena leaderboard dataset

- License: [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/)
- Files: `registry/benchmarks/lmarena.json`; figures in `docs/RESEARCH.md` and `catalog/usecases.yaml`
- Dataset: https://huggingface.co/datasets/lmarena-ai/leaderboard-dataset, `latest` split, revision `f566816dfe80f18d0bc804dd5deafa68d5957cb9`, values as of 2026-11-06 (agent subsets 2026-11-04)
- Citation: LMArena (arena.ai), Arena Leaderboard Dataset, https://huggingface.co/datasets/lmarena-ai/leaderboard-dataset, rev f566816, CC BY 4.0.

## Epoch AI

- License: [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/)
- Files: `registry/benchmarks/epoch_ai.json`; figures in `docs/RESEARCH.md`
- Scope: only GPQA Diamond, MATH Level 5, OTIS Mock AIME and FrontierMath, the benchmarks Epoch's methodology page lists as run by Epoch. Downloaded from https://epoch.ai/data/benchmark_data.zip, retrieved 2026-11-08. External results, the Epoch Capabilities Index and other internal-run files are not used.
- Citation: Epoch AI, 'Capabilities & benchmarking'. Published online at epoch.ai. Retrieved from 'https://epoch.ai/benchmarks' [online resource]. Licensed under CC BY 4.0.

## OpenRouter Data API

- License: CC BY 4.0, as stated in the [OpenRouter Data API documentation](https://openrouter.ai/docs/cookbook/administration/data-api). The data may be redistributed with attribution, but not re-served as a competing free API, and the response `meta` block has to travel with published figures.
- Files: `registry/benchmarks/openrouter_evals.json` (with `api_meta_as_of`); figures in `docs/RESEARCH.md` and `catalog/usecases.yaml`. No raw Data API response is republished.
- Required citation lines for the snapshots used:

| Dataset | Endpoint | Citation |
|---|---|---|
| Model rankings, 30 days | `/api/v1/datasets/rankings-daily` | Source: OpenRouter (openrouter.ai/rankings), as of 2026-11-09T04:00:01.004Z. Licensed under CC BY 4.0. |
| Model rankings, filtered by category, modality and context | `/api/v1/datasets/rankings-daily` | Source: OpenRouter (openrouter.ai/rankings), as of 2026-11-09T04:01:45Z–04:04:22Z. Licensed under CC BY 4.0. |
| Model rankings, week of 2026-11-02 | `/api/v1/datasets/rankings-daily` | Source: OpenRouter (openrouter.ai/rankings), as of 2026-11-09T04:02:06.292Z. Licensed under CC BY 4.0. |
| Task classifications, trailing 7 days | `/api/v1/classifications/task` | Source: OpenRouter (openrouter.ai/rankings), as of 2026-11-08. Licensed under CC BY 4.0. |
| App rankings | `/api/v1/datasets/app-rankings` | Source: OpenRouter (openrouter.ai/apps), as of 2026-11-09T04:00:18.014Z–04:01:35Z. Licensed under CC BY 4.0. |
| Session cost | `/api/v1/datasets/session-cost` | Source: OpenRouter Data API (openrouter.ai/docs/cookbook/administration/data-api), as of 2026-11-02T07:25:14.332Z. Licensed under CC BY 4.0. |
| OpenRouter evals (`source=openrouter` only) | `/api/v1/benchmarks` | Source: OpenRouter evals (openrouter.ai) via OpenRouter (openrouter.ai/rankings), as of 2026-11-09T00:00:58Z. Licensed under CC BY 4.0. |

## OpenRouter public model API

- License: none stated. The [API reference](https://openrouter.ai/docs/api/api-reference/models/list-all-models-and-their-properties.md) gives no license for `/models`, `/models/{id}/endpoints`, `/endpoints/zdr`, `/images/models` or `/videos/models`.
- Use: factual prices, capabilities, modalities, endpoint counts, uptime and data-policy tiers, with attribution and dates, for the 63 referenced models only (`registry/models.json`). The rest is built locally by `registry/scripts/`, not published.
- Attribution: Model metadata from the OpenRouter public API (openrouter.ai/api/v1/models?output_modalities=all), as of 2026-11-09T03:59:17Z; endpoints as of 2026-11-09T04:00:04Z–04:09:50Z.
- The `benchmarks` object in these responses holds third-party scores (Artificial Analysis, Design Arena). It is removed at fetch time and never stored.

## Aider polyglot leaderboard

- License: [Apache-2.0](https://github.com/Aider-AI/aider/blob/main/LICENSE.txt)
- Files: `registry/benchmarks/aider_polyglot.json` (header only; no referenced model appears). Cited as historical method in `docs/RESEARCH.md`.
- Citation: Aider LLM Leaderboards, polyglot benchmark, https://aider.chat/docs/leaderboards/, data from github.com/Aider-AI/aider `aider/website/_data/polyglot_leaderboard.yml` (Apache-2.0). Newest entry 2025-10-03.

## models.dev

- License: MIT ([github.com/sst/models.dev](https://github.com/sst/models.dev))
- Use: cross-check of OpenRouter's prices and limits in local builds (`models-dev-diff.json`), and the Anthropic direct prices cited in `docs/RESEARCH.md`. Nothing from it is in the published snapshot.

## Not used

Artificial Analysis, Design Arena, SWE-bench (every variant), LiveCodeBench, MMMU, Vellum and HELM, because their terms restrict republishing or commercial use or their license is unclear. Nothing from these sources appears in any data field in this repository.
