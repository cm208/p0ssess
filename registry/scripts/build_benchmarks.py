"""Build benchmark score files from what fetch_benchmarks.py and fetch_openrouter.py saved.

Usage: python3 -I build_benchmarks.py <dl_dir> <raw_dir> <out_dir>

<dl_dir> is fetch_benchmarks.py's output; <raw_dir> is fetch_openrouter.py's (for the model list
used to join names to OpenRouter ids). Makes no network calls. Writes epoch_ai.json, lmarena.json,
aider_polyglot.json, openrouter_evals.json (when the evals were fetched) and model_id_map.json.
"""
import csv, json, math, os, re, sys, collections
from datetime import datetime, timezone

if len(sys.argv) != 4:
    sys.exit(__doc__)
DL, RAW, OUT = sys.argv[1:4]
os.makedirs(OUT, exist_ok=True)
try:
    RETRIEVED = open(f"{DL}/retrieved.txt").read().strip()
except OSError:
    RETRIEVED = datetime.now(timezone.utc).strftime("%Y-%m-%d")

FORBIDDEN = ("artificial analysis", "artificial-analysis", "artificial_analysis", "artificialanalysis",
             "design arena", "design-arena", "design_arena", "designarena", "swe-bench", "swe_bench",
             "swebench", "livecodebench", "mmmu", "vellum")

# ---------------- OpenRouter model index ----------------
models = json.load(open(f"{RAW}/models_all.json"))["data"]
or_ids = {}
for m in models:
    or_ids.setdefault(m["id"].split(":")[0], m)


def norm(s):
    s = s.lower()
    s = re.sub(r"\(.*?\)", "", s)
    if ": " in s:
        s = s.split(": ", 1)[1]
    if "/" in s:
        s = s.split("/", 1)[1]
    return re.sub(r"[^a-z0-9]", "", s)


def strip_date(slug):
    return re.sub(r"[-_](20\d{6}|20\d{2}-\d{2}-\d{2})$", "", slug)


index = collections.defaultdict(set)
canon = {}
for base, m in or_ids.items():
    index[norm(base)].add((base, "openrouter_id"))
    cs = m.get("canonical_slug") or ""
    if cs:
        canon[cs] = base
        index[norm(cs)].add((base, "canonical_slug"))
        index[norm(strip_date(cs))].add((base, "canonical_slug_dateless"))
    index[norm(m.get("name", ""))].add((base, "display_name"))


def lookup(*cands):
    """(openrouter_id, method, ambiguous candidates) for the first name with a unique hit."""
    for c in cands:
        if not c:
            continue
        for variant in (c, strip_date(c)):
            k = norm(variant)
            if not k:
                continue
            hits = {b for b, _ in index.get(k, set())}
            if len(hits) == 1:
                b = next(iter(hits))
                meth = sorted({mm for bb, mm in index[k] if bb == b})[0]
                if variant != c:
                    meth += "+date_stripped"
                return b, meth, None
            if len(hits) > 1:
                exact = [b for b in hits if norm(b) == k]
                if len(exact) == 1:
                    return exact[0], "openrouter_id", None
                return None, "ambiguous", sorted(hits)
    return None, "unmatched", None


mapping, map_seen = [], {}


def map_model(source, name, variant, *cands):
    key = (source, name)
    if key in map_seen:
        return map_seen[key]
    oid, meth, amb = lookup(*cands)
    row = {"source": source, "source_model": name, "variant": variant, "openrouter_id": oid, "match_method": meth,
           "confidence": ("high" if meth.startswith(("openrouter_id", "canonical_slug")) else
                          "medium" if meth.startswith("display_name") else "none")}
    if amb:
        row["ambiguous_candidates"] = amb
    mapping.append(row)
    map_seen[key] = oid
    return oid


def binom_se(p, n):
    if p is None or not n:
        return None
    p = max(0.0, min(1.0, p))
    return round(math.sqrt(p * (1 - p) / n), 4)


def dump(name, header, records):
    for r in records:
        low = json.dumps(r).lower()
        for bad in FORBIDDEN:
            assert bad not in low, (name, bad, r.get("source_model"))
    with open(f"{OUT}/{name}", "w") as f:
        f.write("{\n" + ",\n".join(f'"{k}": {json.dumps(v)}' for k, v in header.items()) + ',\n"records": [\n')
        f.write(",\n".join(json.dumps(r, ensure_ascii=False) for r in records))
        f.write("\n]}\n")


def num(v):
    try:
        return float(v)
    except (TypeError, ValueError):
        return None


# ---------------- Epoch AI (self-run benchmarks on the methodology page only) ----------------
EPOCH_LIC = "CC BY 4.0"
EPOCH_CIT = ("Epoch AI, 'Capabilities & benchmarking'. Published online at epoch.ai. Retrieved from "
             "'https://epoch.ai/benchmarks' [online resource]. Licensed under CC BY 4.0.")
EPOCH_FILES = {
    "gpqa_diamond": ("GPQA Diamond", ["science_qa", "reasoning"]),
    "math_level_5": ("MATH Level 5", ["math"]),
    "otis_mock_aime_2024_2025": ("OTIS Mock AIME 2024-2025", ["math"]),
    "frontiermath": ("FrontierMath-2025-02-28-Private (superseded)", ["math"]),
    "frontiermath_tier_4": ("FrontierMath-Tier-4-2025-07-01-Private (superseded)", ["math"]),
    "frontiermath_tiers_1_3_v2": ("FrontierMath Tiers 1-3 v2 (private)", ["math"]),
    "frontiermath_tier_4_v2": ("FrontierMath Tier 4 v2 (private)", ["math"]),
}
meta_rows = {r["model_version"]: r for r in csv.DictReader(open(f"{DL}/epoch/model_metadata.csv")) if r["model_version"]}
bmeta = {r["source_file"]: r for r in csv.DictReader(open(f"{DL}/epoch/benchmark_metadata.csv")) if r["source_file"]}
epoch_recs = []
for fn, (bname, fams) in EPOCH_FILES.items():
    path = f"{DL}/epoch/{fn}.csv"
    if not os.path.exists(path):
        print("epoch: missing", fn)
        continue
    bm = bmeta.get(fn + ".csv", {})
    for r in csv.DictReader(open(path)):
        v = r["Best score (across scorers)"]
        if not v:
            continue
        mv = r["Model version"]
        base, _, effort = mv.partition("_")
        group = meta_rows.get(mv, {}).get("model_group") or ""
        cands = [base, re.sub(r"-(together|fireworks|deepinfra|bedrock|vertex|azure)$", "", base), group,
                 re.sub(r"(\d)-(\d)", r"\1.\2", base)]
        oid = map_model("epoch", mv, effort or None, *cands)
        se = r.get("stderr")
        epoch_recs.append({
            "source": "Epoch AI Benchmarking Hub (internal runs)", "license": EPOCH_LIC, "citation": EPOCH_CIT,
            "as_of": (r.get("Started at") or "")[:10] or None, "retrieved": RETRIEVED,
            "benchmark": bname, "benchmark_file": fn + ".csv",
            "listed_as_epoch_run_on_methodology_page": True,
            "superseded_by": bm.get("superseded_by") or None,
            "task_families": fams,
            "source_model": mv, "model_group": group or None, "variant": effort or None,
            "openrouter_id": oid,
            "metric": "best_score_across_scorers", "value": round(float(v), 4),
            "mean_score": round(float(r["mean_score"]), 4) if r.get("mean_score") else None,
            "stderr": round(float(se), 4) if se else None,
            "random_baseline": float(bm["random_baseline"]) if bm.get("random_baseline") else None,
            "has_inspect_logs": bool(r.get("Log viewer") or r.get("Logs")),
        })
dump("epoch_ai.json", {
    "source": "Epoch AI Benchmarking Hub, internal runs only", "license": EPOCH_LIC,
    "license_url": "https://creativecommons.org/licenses/by/4.0/", "citation": EPOCH_CIT,
    "download": "https://epoch.ai/data/benchmark_data.zip", "retrieved": RETRIEVED,
    "excluded": ["all *_external.csv files (externally sourced, original licenses apply)",
                 "swe_bench_verified.csv (SWE-bench is excluded by p0ssess policy)",
                 "epoch_capabilities_index/* (composite built partly from external results)",
                 "internal-run files not named on Epoch's methodology page"],
    "notes": "value = 'Best score (across scorers)', fraction 0-1. as_of = run start date. variant = reasoning effort suffix from Epoch's model version.",
}, epoch_recs)

# ---------------- LMArena ----------------
LM_LIC = "CC BY 4.0"
try:
    LM_REV = json.load(open(f"{DL}/lmarena/revision.json")).get("sha") or ""
except OSError:
    LM_REV = ""
LM_CIT = (f"LMArena leaderboard-dataset (huggingface.co/datasets/lmarena-ai/leaderboard-dataset, rev {LM_REV[:7]}), CC BY 4.0"
          if LM_REV else "LMArena leaderboard-dataset (huggingface.co/datasets/lmarena-ai/leaderboard-dataset), CC BY 4.0")
LM_SUBSETS = {
    "text_style_control": ["chat_general"], "text_factuality": ["factual_recall"],
    "vision_style_control": ["vision_understanding"], "document_style_control": ["document_understanding"],
    "search_style_control": ["web_search_answering"], "search_factuality": ["web_search_answering", "factual_recall"],
    "webdev": ["web_frontend_coding"], "agent": ["agentic_coding"],
    "agent_bash_reliability": ["agentic_coding"], "agent_praise_complaint": ["agentic_coding"],
    "agent_steerability": ["agentic_coding"], "agent_task_outcome_explicit": ["agentic_coding"],
    "agent_tool_hallucination": ["agentic_coding"],
    "text_to_image": ["image_generation"], "image_edit": ["image_editing"],
    "text_to_video": ["video_generation"], "image_to_video": ["video_generation"], "video_edit": ["video_editing"],
}
CAT_FAMILY = {
    "coding": "coding", "creative_writing": "creative_writing", "math": "math",
    "instruction_following": "instruction_following", "multi_turn": "multi_turn_chat",
    "longer_query": "long_prompt", "hard_prompts": "hard_reasoning", "hard_prompts_english": "hard_reasoning",
    "expert": "expert_qa", "ocr": "ocr", "captioning": "image_captioning", "diagram": "diagram_understanding",
    "homework": "vision_homework", "entity_recognition": "vision_entity_recognition",
    "creative_writing_vision": "creative_writing", "humor": "humor",
    "chinese": "multilingual", "french": "multilingual", "german": "multilingual", "japanese": "multilingual",
    "korean": "multilingual", "polish": "multilingual", "russian": "multilingual", "spanish": "multilingual",
    "non_english": "multilingual",
}


def r2(v, n):
    return round(v, n) if v is not None else None


lm_recs = []
for sub, fams in LM_SUBSETS.items():
    path = f"{DL}/lmarena/{sub}.json"
    if not os.path.exists(path):
        print("lmarena: missing subset", sub)
        continue
    for r in json.load(open(path))["rows"]:
        name = r["model_name"]
        m = re.search(r"\(([^)]*)\)\s*$", name)
        variant = m.group(1) if m else None
        oid = map_model("lmarena", name, variant, name)
        cat = r["category"]
        f2 = list(fams)
        if cat.startswith("industry_"):
            f2.append("domain:" + cat[len("industry_"):])
        elif cat in CAT_FAMILY:
            f2.append(CAT_FAMILY[cat])
        rec = {"source": "LMArena leaderboard dataset", "license": LM_LIC, "citation": LM_CIT,
               "as_of": r["leaderboard_publish_date"], "retrieved": RETRIEVED,
               "subset": sub, "category": cat, "task_families": f2,
               "source_model": name, "organization": r.get("organization"), "variant": variant,
               "openrouter_id": oid, "rank": r.get("rank")}
        if "rating" in r:
            rec.update(metric="bradley_terry_rating", value=r2(r["rating"], 2),
                       ci_lower=r2(r.get("rating_lower"), 2), ci_upper=r2(r.get("rating_upper"), 2),
                       votes=r.get("vote_count"))
        else:
            rec.update(metric="ips_score", value=r2(r["score"], 4),
                       ci_lower=r2(r.get("score_ci_lower"), 4), ci_upper=r2(r.get("score_ci_upper"), 4),
                       observations=r.get("observation_count"), sessions=r.get("session_count"),
                       cost_per_task_p50=r.get("cost_per_task_p50"),
                       output_tokens_per_task_p50=r.get("output_tokens_per_task_p50"))
        lm_recs.append(rec)
dump("lmarena.json", {
    "source": "LMArena leaderboard dataset (latest split)", "license": LM_LIC,
    "license_url": "https://creativecommons.org/licenses/by/4.0/", "citation": LM_CIT,
    "dataset": "https://huggingface.co/datasets/lmarena-ai/leaderboard-dataset", "dataset_revision": LM_REV or None,
    "retrieved": RETRIEVED,
    "notes": "Style-controlled subsets kept for text/vision/document/search (the arena default since 2025-05-16); raw non-style-control duplicates left out. Ratings are only comparable within one subset+category. Agent subsets use IPS scores, not Bradley-Terry.",
}, lm_recs)


# ---------------- Aider ----------------
def scalar(v):
    v = v.strip()
    if v[:1] in "\"'":
        q = v[0]
        return v[1:v.index(q, 1)] if q in v[1:] else v[1:]
    v = re.sub(r"\s+#.*$", "", v)
    if v in ("", "null", "~"):
        return None
    if v in ("true", "false"):
        return v == "true"
    if re.fullmatch(r"-?\d+", v):
        return int(v)
    if re.fullmatch(r"-?\d+\.\d*(e-?\d+)?", v):
        return float(v)
    return v


def parse_flat_yaml_list(text):
    """Aider's leaderboard file: a list of flat `key: value` mappings."""
    out, cur = [], None
    for line in text.splitlines():
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        if line.startswith("- "):
            cur = {}
            out.append(cur)
            line = line[2:]
        k, sep, v = line.strip().partition(":")
        if sep and cur is not None:
            cur[k.strip()] = scalar(v)
    return out


AI_LIC = "Apache-2.0"
AI_CIT = ("Aider LLM Leaderboards, polyglot benchmark, github.com/Aider-AI/aider "
          "aider/website/_data/polyglot_leaderboard.yml (Apache-2.0), https://aider.chat/docs/leaderboards/")
ai_recs = []
for r in parse_flat_yaml_list(open(f"{DL}/aider/polyglot_leaderboard.yml").read()):
    name = str(r["model"])
    m = re.search(r"\(([^)]*)\)\s*$", name)
    composite = "+" in name or r.get("edit_format") == "architect"
    oid = None if composite else map_model("aider", name, m.group(1) if m else None, name)
    if composite:
        mapping.append({"source": "aider", "source_model": name, "variant": None, "openrouter_id": None,
                        "match_method": "composite_not_mapped", "confidence": "none"})
    p = r["pass_rate_2"] / 100.0
    ai_recs.append({"source": "Aider polyglot leaderboard", "license": AI_LIC, "citation": AI_CIT,
                    "as_of": str(r.get("date")), "retrieved": RETRIEVED,
                    "benchmark": "Aider polyglot (225 Exercism exercises, 6 languages)",
                    "task_families": ["coding", "code_editing"],
                    "source_model": name, "variant": m.group(1) if m else None, "openrouter_id": oid,
                    "composite_setup": composite, "edit_format": r.get("edit_format"),
                    "metric": "pass_rate_2", "value": r["pass_rate_2"], "pass_rate_1": r.get("pass_rate_1"),
                    "test_cases": r.get("test_cases"),
                    "binomial_se_pct": round(100 * binom_se(p, r.get("test_cases")), 2) if r.get("test_cases") else None,
                    "percent_cases_well_formed": r.get("percent_cases_well_formed"),
                    "total_cost_usd": r.get("total_cost"), "seconds_per_case": r.get("seconds_per_case"),
                    "reasoning_effort": r.get("reasoning_effort"),
                    "aider_version": str(r["versions"]) if r.get("versions") is not None else None})
latest = max(str(r["as_of"]) for r in ai_recs)
dump("aider_polyglot.json", {
    "source": "Aider polyglot leaderboard", "license": AI_LIC,
    "license_url": "https://github.com/Aider-AI/aider/blob/main/LICENSE.txt",
    "citation": AI_CIT, "retrieved": RETRIEVED, "latest_entry_date": latest,
    "notes": f"Historical: newest entry {latest}. Use only as a historical prior. binomial_se_pct is computed by this build script, not published by Aider.",
}, ai_recs)

# ---------------- OpenRouter evals ----------------
OR_LIC = "CC BY 4.0"
OR_CIT = "Source: OpenRouter evals (openrouter.ai) via OpenRouter (openrouter.ai/rankings). Licensed under CC BY 4.0."
OR_FAM = {"gpqa_diamond": ["science_qa", "reasoning"], "tau_bench_verified_airline": ["agentic_tool_use"],
          "search_browsecomp": ["web_research"], "search_dsqa": ["web_research"],
          "search_widesearch": ["web_research"], "search_hle": ["web_research", "expert_qa"]}
or_recs = []
or_path = f"{DL}/or/benchmarks_openrouter_only.json"
if os.path.exists(or_path):
    orj = json.load(open(or_path))
    for r in orj["data"]:
        if r.get("source") != "openrouter":
            continue
        slug = r["model_permaslug"]
        oid = canon.get(slug) or (slug if slug in or_ids else None)
        if oid:
            mapping.append({"source": "openrouter_evals", "source_model": slug, "variant": None, "openrouter_id": oid,
                            "match_method": "permaslug=canonical_slug" if slug in canon else "permaslug=openrouter_id",
                            "confidence": "high"})
        else:
            oid = map_model("openrouter_evals", slug, None, slug, r.get("display_name"))
        val = r.get("accuracy") if r.get("accuracy") is not None else r.get("primary_score")
        or_recs.append({"source": "OpenRouter evals (Data API /api/v1/benchmarks, source=openrouter)", "license": OR_LIC,
                        "citation": OR_CIT, "as_of": r["last_run_timestamp"][:10], "retrieved": RETRIEVED,
                        "benchmark": r["benchmark_type"], "task_families": OR_FAM.get(r["benchmark_type"], ["unclassified"]),
                        "source_model": slug, "display_name": r.get("display_name"), "openrouter_id": oid,
                        "metric": r.get("primary_metric") or "accuracy", "value": round(val, 4) if val is not None else None,
                        "stddev_published": r.get("accuracy_stddev"), "total_tasks": r.get("total_tasks"),
                        "binomial_se_computed": binom_se(val, r.get("total_tasks")) if r.get("primary_metric") in (None, "accuracy") else None,
                        "avg_cost_per_task_usd": r.get("avg_cost_per_task"), "avg_latency_per_task_ms": r.get("avg_latency_per_task_ms"),
                        "search_engine": r.get("search_engine"), "search_surface": r.get("search_surface")})
    dump("openrouter_evals.json", {
        "source": "OpenRouter Data API /api/v1/benchmarks, rows with source=openrouter only", "license": OR_LIC,
        "license_url": "https://openrouter.ai/docs/cookbook/administration/data-api", "citation": OR_CIT,
        "api_meta_as_of": (orj.get("meta") or {}).get("as_of"), "retrieved": RETRIEVED,
        "excluded": "Every row whose source is not openrouter was discarded before saving.",
        "notes": "binomial_se_computed is derived by this build script from value and total_tasks; not published by OpenRouter.",
    }, or_recs)
else:
    print("openrouter evals not fetched (needs OPENROUTER_API_KEY): openrouter_evals.json not written")

# ---------------- mapping + coverage ----------------
seen, uniq = set(), []
for m in mapping:
    k = (m["source"], m["source_model"])
    if k not in seen:
        seen.add(k)
        uniq.append(m)
srcs = {"epoch": epoch_recs, "lmarena": lm_recs, "aider": ai_recs, "openrouter_evals": or_recs}


def covered(oid, s, recs, subset_only):
    hits = [r for r in recs if r["openrouter_id"] == oid]
    if s == "lmarena" and subset_only:
        return sorted({r["subset"] for r in hits})
    return sorted({(r.get("benchmark") or r.get("subset")) for r in hits})


out = {"generated": RETRIEVED,
       "method": "Normalized-name join (lowercase, drop parenthetical effort, drop 'Provider: ' prefix, strip non-alphanumerics), tried against OpenRouter id, canonical_slug (with and without date) and display name. Unique hit = match; multiple = ambiguous (not joined). OpenRouter eval permaslugs join on canonical_slug exactly. Reasoning-effort variants (High/Max/xHigh...) map to the same OpenRouter id; effort is a request parameter on OpenRouter.",
       "openrouter_models_snapshot": "https://openrouter.ai/api/v1/models?output_modalities=all (%d base ids)" % len(or_ids),
       "mappings": uniq}
rk_path = f"{DL}/or/rankings_daily.json"
if os.path.exists(rk_path):
    rk = json.load(open(rk_path))
    tok = collections.Counter()
    for r in rk["data"]:
        tok[r["model_permaslug"]] += int(float(r["total_tokens"]))
    cover = []
    for slug, t in tok.most_common(40):
        b0 = slug.split(":")[0]
        oid = canon.get(b0) or (b0 if b0 in or_ids else canon.get(strip_date(b0)) or strip_date(b0))
        row = {"openrouter_id": oid, "permaslug": slug, "tokens_30d": t}
        for s, recs in srcs.items():
            row[s] = covered(oid, s, recs, True)
        cover.append(row)
    out["coverage_top40_by_tokens_30d"] = {
        "source": "OpenRouter Data API /api/v1/datasets/rankings-daily, %s to %s" % (rk["meta"]["start_date"], rk["meta"]["end_date"]),
        "license": "CC BY 4.0",
        "citation": "Source: OpenRouter (openrouter.ai/rankings), as of %s. Licensed under CC BY 4.0." % rk["meta"]["as_of"],
        "rows": cover}
out["coverage_claude"] = [dict({"openrouter_id": oid}, **{s: covered(oid, s, recs, False) for s, recs in srcs.items()})
                          for oid in or_ids if oid.startswith("anthropic/")]
json.dump(out, open(f"{OUT}/model_id_map.json", "w"), indent=1)

for s, recs in srcs.items():
    print(s, "records", len(recs), "mapped", sum(1 for r in recs if r["openrouter_id"]))
