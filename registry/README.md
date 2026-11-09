# Registry snapshot

A small, dated snapshot of the facts the Predictive Model Engine (PME) routes on: prices, capabilities, endpoints and clean benchmark scores. It covers only the **63 models that [catalog/usecases.yaml](../catalog/usecases.yaml) references** (recommended, alternatives, ensemble and pipeline steps). It is not a mirror of OpenRouter. OpenRouter states no license for its model metadata, so the full registry is something you build locally with the scripts here.

Snapshot as of **2026-11-09** (UTC). Every file has a `meta` or header block that names its sources, and every record carries `src` or `source` plus `as_of`. Licenses and required citation lines are in [ATTRIBUTION.md](ATTRIBUTION.md).

## Files

| File | Contents |
|---|---|
| `models.json` | One record per referenced model: modalities, context, max output, capability flags, model-level prices, an endpoint summary (provider count, uptime, data-policy tier, cheapest endpoint and cheapest ZDR endpoint), and the catalog ids that reference it |
| `benchmarks/lmarena.json` | LMArena ratings for the referenced models (CC BY 4.0), dataset revision f566816 |
| `benchmarks/epoch_ai.json` | Epoch AI scores for GPQA Diamond, MATH Level 5, OTIS Mock AIME and FrontierMath only, the four benchmarks Epoch's methodology page lists as run by Epoch (CC BY 4.0). Superseded FrontierMath versions are left out |
| `benchmarks/openrouter_evals.json` | OpenRouter's own evals (`source=openrouter` only; CC BY 4.0) |
| `benchmarks/aider_polyglot.json` | Aider polyglot (Apache-2.0). Historical only: the newest entry is from 2025-10-03, so no referenced model appears and the file holds no records |
| `scripts/` | Rebuild scripts (Python 3 standard library only) |

## `models.json` fields

- `id`, `name`, `author`, `kind` (text, text+image, audio, image, video, speech, transcription, embeddings, rerank, decisions; derived from output modalities), `hugging_face_id` (present for most open-weight models)
- `input_modalities`, `output_modalities`, `api_routes`: verbatim from OpenRouter. `file` means PDF or file input. Media models are served on their own routes (`/images`, `/videos`, `/audio/speech`, `/audio/transcriptions`, `/embeddings`, `/rerank`).
- `context_length`, `max_output_tokens`
- `caps`: `tools`, `tool_choice`, `parallel_tool_calls`, `structured_outputs`, `json_mode`, `reasoning`, `reasoning_mandatory`, `reasoning_efforts`, `web_search_options`, `batch_api`. A flag is true when **at least one** endpoint supports it.
- `pricing` (USD; `*_per_m` is per 1M tokens): `input_per_m`, `output_per_m`, cache, reasoning, audio and image fields, `web_search_usd`, and `long_context_overrides` (higher prices above `min_prompt_tokens`; Claude Haiku 5.5 rises from $0.10/$0.50 to $0.50/$2.50 above 100k prompt tokens). Speech models carry `prompt_usd_raw` and `completion_usd_raw` with a `unit_note`, because OpenRouter does not say whether a value is per character, per second or per token. Video models carry raw SKUs. `image_out_raw` is kept raw because its unit is unresolved.
- `endpoints`:
  - `provider_count`, `endpoint_count`, `providers`, `service_tiers`
  - `uptime_1d_best`, `uptime_1d_worst`: OpenRouter's one-day uptime, the share of minutes in which at least 80% of attempts succeeded, counting only minutes with 10 or more attempts. Null means too little traffic to measure, not down.
  - `data_policy_counts`, `best_data_policy`, `has_zdr_endpoint` (see data policy below)
  - `cheapest` and `cheapest_zdr`: the endpoint with the lowest (3 × input + output) / 4 price, with provider, tag, tier, prices, context, data policy and uptime. **The model-level price is not always the cheapest endpoint**: Gemini 3.8 Flash lists $0.825/$4.125, while its flex tier costs $0.375/$1.875.
- `referenced_by`: catalog ids that name the model
- `src`, `as_of`

### Data policy

- `zdr`: on OpenRouter's `/endpoints/zdr` list without safety retention. No routine storage, no training.
- `safety_retention`: on that list with safety retention. Requests may be kept only to prevent abuse, never trained on.
- `not_in_zdr_list`: everything else. This **does not** mean the provider trains on prompts; every first-party Anthropic and OpenAI endpoint is here, and both companies' API terms exclude training. The public API has no per-endpoint training flag. To keep training providers out, send `provider.data_collection: "deny"`; for zero retention, send `zdr: true`.

## Benchmark records

Every record carries `source`, `license`, `citation`, `as_of` (the date of that value) and `retrieved`, plus `openrouter_id` for the join.

- **LMArena:** `value` is a style-controlled Bradley-Terry rating with 95% bounds (`ci_lower`, `ci_upper`) and `votes`. Ratings compare only within one `subset` and `category`. Agent subsets use IPS scores (`metric: ips_score`) with observation counts and median cost per task.
- **Epoch AI:** `value` is "best score across scorers", a 0–1 fraction, with Epoch's `stderr`. `variant` is the reasoning effort (`max`, `high`, …), so scores are upper bounds for default-effort calls. Key evidence by `(model, effort)`.
- **OpenRouter evals:** `value` is accuracy (F1 for `search_widesearch`), `total_tasks` is the sample size, `avg_cost_per_task_usd` is OpenRouter's measured cost. `binomial_se_computed` is derived here as sqrt(p(1−p)/n), not published by OpenRouter.
- **Joins:** names are normalized and matched against OpenRouter's id, canonical slug and display name; ambiguous names are left unjoined. Effort variants map to one OpenRouter id, because effort is a request parameter there. Display-name matches deserve a human look.

## Rebuilding

No key is needed for models, endpoints, ZDR, the image and video catalogs, models.dev, Epoch, LMArena or Aider. OpenRouter's Data API (rankings and its own evals) needs a key, read only from `OPENROUTER_API_KEY` and never written to disk.

```sh
python3 -I registry/scripts/fetch_openrouter.py build/raw            # ~1 call per second for endpoints
python3 -I registry/scripts/build_registry.py build/raw build/registry
python3 -I registry/scripts/fetch_benchmarks.py build/bench-dl
python3 -I registry/scripts/build_benchmarks.py build/bench-dl build/raw build/benchmarks
python3 -I registry/scripts/snapshot.py build/registry build/benchmarks catalog/usecases.yaml registry
```

- `fetch_openrouter.py [--limit N]` removes every `benchmarks` object from OpenRouter responses before writing; it carries third-party scores whose terms do not allow reuse.
- `build_registry.py` writes the full local registry: `models.json` (all models), `endpoints-uptime.json` (one row per provider endpoint), `models-dev-diff.json` (a cross-check against models.dev), and `usage-week.json` when the rankings were fetched.
- `fetch_benchmarks.py [--subsets a,b]` downloads only the allowed Epoch files, LMArena's `latest` split through the Hugging Face rows API, and Aider's YAML. With a key it also saves OpenRouter's evals, keeping only `source=openrouter` rows.
- `build_benchmarks.py` joins scores to OpenRouter ids and fails if any record names an excluded source.
- `snapshot.py` filters the full builds to the models the catalog references and fails if a referenced model is missing.

A rebuild picks up new data: LMArena published a newer revision after this snapshot, so rebuilt ratings differ slightly from the ones here.

## Excluded sources

Artificial Analysis, Design Arena, SWE-bench (every variant), LiveCodeBench, MMMU, Vellum and HELM are not used. Neither are Epoch's externally sourced files, Epoch's other internal runs that its methodology page does not list, or the Epoch Capabilities Index. The `benchmarks` object in OpenRouter's model API, which holds Artificial Analysis and Design Arena scores, is removed at fetch time.

## Known gaps

- Speech-to-text, text-to-speech and image-output price units are not documented by OpenRouter.
- Rerank models show 0/0 prices in the public API, so no usable rerank price exists.
- Some media prices (Lyria clips and songs) appear only in description text.
- Provider retention periods are not in the public API.
- Latency and throughput need a key and were not collected.
- Audio, video and long-context understanding, transcription, TTS and embeddings have no clean benchmark.
