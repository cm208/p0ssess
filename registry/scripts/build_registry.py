"""Build the full local registry from the files fetch_openrouter.py saved.

Usage: python3 -I build_registry.py <raw_dir> <out_dir>
Reads only <raw_dir>; makes no network calls. Writes models.json, endpoints-uptime.json,
models-dev-diff.json and, when rankings_week.json is present, usage-week.json.
Third-party benchmark scores are dropped everywhere, and the outputs are checked for them.
"""
import glob, json, os, statistics, sys, collections as C
from datetime import datetime, timezone

if len(sys.argv) != 3:
    sys.exit(__doc__)
RAW, OUT = sys.argv[1], sys.argv[2]
os.makedirs(OUT, exist_ok=True)

FETCHED = open(f"{RAW}/fetched_at.txt").read().strip()          # models list fetch time
AS_OF = FETCHED[:10]

def load(p):
    return json.load(open(p))

models_all = load(f"{RAW}/models_all.json")["data"]
topweekly = [m["id"] for m in load(f"{RAW}/models_all_topweekly.json")["data"]]
images_cat = {m["id"]: m for m in load(f"{RAW}/images_models.json")["data"]}
videos_cat = {m["id"]: m for m in load(f"{RAW}/videos_models.json")["data"]}
zdr_list = load(f"{RAW}/endpoints_zdr.json")["data"]
rank = load(f"{RAW}/rankings_week.json") if os.path.exists(f"{RAW}/rankings_week.json") else None
mdev = load(f"{RAW}/modelsdev.json")

ep_files = {}
ep_times = []
for f in glob.glob(f"{RAW}/endpoints/*.json"):
    d = load(f)
    data = d.get("data") or {}
    mid = data.get("id")
    ep_times.append(d.get("_fetched_at"))
    # file name encodes the model id we requested
    ep_files[os.path.basename(f)] = {"data": data, "fetched_at": d.get("_fetched_at"), "url": d.get("_url")}
ep_times = sorted(t for t in ep_times if t)
def ep_key(mid):  # same file naming as fetch_endpoints.py
    return mid.replace("/", "__").replace(":", "~") + ".json"

LIC_OR = ("OpenRouter public API (no key). Factual price/capability data; the API docs state no "
          "open license for /models or /endpoints, so values are used only as attributed "
          "facts. Do not mirror wholesale.")
LIC_DS = ("CC BY 4.0 (stated in the OpenRouter API reference for /datasets/*). Required citation: "
          "\"Source: OpenRouter (openrouter.ai/rankings), as of {as_of}.\"")
LIC_MD = "MIT (models.dev, github.com/sst/models.dev)."

SOURCES = {
    "or_models": {"url": "https://openrouter.ai/api/v1/models?output_modalities=all",
                  "license_note": LIC_OR, "as_of": FETCHED},
    "or_models_topweekly": {"url": "https://openrouter.ai/api/v1/models?output_modalities=all&sort=top-weekly",
                            "license_note": LIC_OR + " Only the order is used (a usage rank), no token counts.",
                            "as_of": FETCHED},
    "or_endpoints": {"url": "https://openrouter.ai/api/v1/models/{canonical_slug}/endpoints (per model, the API's own links.details path)",
                     "license_note": LIC_OR,
                     "as_of": f"{ep_times[0]} .. {ep_times[-1]}" if ep_times else None},
    "or_endpoints_zdr": {"url": "https://openrouter.ai/api/v1/endpoints/zdr",
                         "license_note": LIC_OR, "as_of": FETCHED},
    "or_images_models": {"url": "https://openrouter.ai/api/v1/images/models", "license_note": LIC_OR, "as_of": FETCHED},
    "or_videos_models": {"url": "https://openrouter.ai/api/v1/videos/models", "license_note": LIC_OR, "as_of": FETCHED},
    "or_rankings_week": {"url": ("https://openrouter.ai/api/v1/datasets/rankings-daily?start_date=%s&end_date=%s&period=week"
                                 % (rank["meta"].get("start_date"), rank["meta"].get("end_date"))) if rank else None,
                         "license_note": LIC_DS.format(as_of=rank["meta"]["as_of"]) if rank else LIC_DS, "as_of": rank["meta"]["as_of"] if rank else None},
    "or_docs": {"url": "https://openrouter.ai/docs/llms.txt (field meanings: models, endpoints, zdr, provider-logging, limits, free variant, tts, stt, pdfs)",
                "license_note": "OpenRouter documentation; cited, not copied.", "as_of": FETCHED},
    "models_dev": {"url": "https://models.dev/api.json", "license_note": LIC_MD, "as_of": FETCHED},
}

def per_m(v):
    """USD/token string -> USD per 1M tokens (float). -1 (router) -> None."""
    if v is None:
        return None
    try:
        f = float(v)
    except (TypeError, ValueError):
        return None
    if f < 0:
        return None
    return round(f * 1e6, 6)

def raw_f(v):
    try:
        f = float(v)
    except (TypeError, ValueError):
        return None
    return None if f < 0 else f

OUTPUT_KIND = {}
def kind_of(m):
    o = m["architecture"]["output_modalities"]
    for k in ("video", "speech", "transcription", "embeddings", "rerank", "decisions"):
        if k in o:
            return k
    if "audio" in o:
        return "audio"
    if "image" in o:
        return "image" if "text" not in o else "text+image"
    return "text"

def pricing_block(m, kind):
    p = m.get("pricing") or {}
    blk = {"unit_note": None}
    if kind in ("speech",):
        blk["unit_note"] = ("TTS: OpenRouter docs say most TTS models are priced per character of input "
                            "text (prompt field); some per second of generated audio (completion field).")
        blk["prompt_usd_raw"] = raw_f(p.get("prompt"))
        blk["completion_usd_raw"] = raw_f(p.get("completion"))
        return blk
    if kind in ("transcription",):
        blk["unit_note"] = ("STT: docs say duration-based models are priced per second of audio, newer ones "
                            "per token; the API does not say which applies per model, so raw values are kept.")
        blk["prompt_usd_raw"] = raw_f(p.get("prompt"))
        blk["completion_usd_raw"] = raw_f(p.get("completion"))
        return blk
    if kind == "video":
        v = videos_cat.get(m["id"])
        blk["unit_note"] = ("Video generation SKUs from /videos/models, raw strings as floats. Units are encoded in the SKU "
                            "name: duration_seconds* = USD per output second; cents_* = US cents; video_tokens* = USD per "
                            "video token. usd_per_second_min/max use only the per-second SKUs.")
        skus = (v.get("pricing_skus") or {}) if v else {}
        blk["video_pricing_skus_raw"] = {k: raw_f(x) for k, x in skus.items()} or None
        ps = []
        for k, x in skus.items():
            f = raw_f(x)
            if f is None:
                continue
            if k.startswith("duration_seconds") or "_duration_seconds" in k:
                ps.append(f)
            elif k.startswith("cents_per") and "second" in k and "megapixel" not in k:
                ps.append(f / 100)
        blk["usd_per_second_min"] = min(ps) if ps else None
        blk["usd_per_second_max"] = max(ps) if ps else None
        if per_m(p.get("prompt")) or per_m(p.get("completion")):
            blk["input_per_m"] = per_m(p.get("prompt"))
            blk["output_per_m"] = per_m(p.get("completion"))
            blk["web_search_usd"] = raw_f(p.get("web_search"))
        return {k: v for k, v in blk.items() if v is not None}
    blk.update({
        "input_per_m": per_m(p.get("prompt")),
        "output_per_m": per_m(p.get("completion")),
        "cache_read_per_m": per_m(p.get("input_cache_read")),
        "cache_write_per_m": per_m(p.get("input_cache_write")),
        "cache_write_1h_per_m": per_m(p.get("input_cache_write_1h")),
        "reasoning_per_m": per_m(p.get("internal_reasoning")),
        "audio_in_per_m": per_m(p.get("audio")),
        "audio_in_cached_per_m": per_m(p.get("input_audio_cache")),
        "audio_out_per_m": per_m(p.get("audio_output")),
        "image_in_usd_each": raw_f(p.get("image")),        # docs: "Price in USD per input image"
        "image_token_per_m": per_m(p.get("image_token")),  # docs: "Price in USD per image token"
        "image_out_raw": raw_f(p.get("image_output")),     # docs: "Price in USD per output image"; see README caveat
        "request_usd": raw_f(p.get("request")),
        "web_search_usd": raw_f(p.get("web_search")),
        "long_context_overrides": [
            {"min_prompt_tokens": o.get("min_prompt_tokens"),
             **{f"{k}_per_m": per_m(o.get(k)) for k in ("prompt", "completion", "input_cache_read", "input_cache_write") if k in o},
             **({"other_conditions": sorted(set(o) - {"min_prompt_tokens", "prompt", "completion", "input_cache_read", "input_cache_write", "input_cache_write_1h"})}
                if set(o) - {"min_prompt_tokens", "prompt", "completion", "input_cache_read", "input_cache_write", "input_cache_write_1h"} else {})}
            for o in (p.get("overrides") or [])
        ] or None,
    })
    if p.get("prompt") == "-1":
        blk["unit_note"] = "Router model: price is the routed model's price (listed as -1)."
    blk = {k: v for k, v in blk.items() if v is not None}
    return blk

# ---- ZDR / data policy index
zdr_idx = {(e["model_id"], e["tag"]): e for e in zdr_list}

def policy_of(model_id, tag):
    e = zdr_idx.get((model_id, tag))
    if e is None:
        return "not_in_zdr_list"
    return "safety_retention" if e.get("safety_retention") else "zdr"

# ---- usage rank
rank_pos = {mid: i + 1 for i, mid in enumerate(topweekly)}
weekly_tokens = C.Counter()
for r in (rank or {}).get("data", []):
    weekly_tokens[r["model_permaslug"]] += int(r["total_tokens"])
slug_to_id = {}
for m in models_all:  # prefer the base id over :batch/:free variants that share a canonical slug
    cs = m.get("canonical_slug")
    if cs and (cs not in slug_to_id or (":" in slug_to_id[cs] and ":" not in m["id"])):
        slug_to_id[cs] = m["id"]

MEDIA_OUT = {"image", "audio", "video", "speech", "transcription"}
def media_capable(m):
    i = set(m["architecture"]["input_modalities"]); o = set(m["architecture"]["output_modalities"])
    return bool(i & {"audio", "video"}) or bool(o & MEDIA_OUT)

representative = set(topweekly[:60]) | {m["id"] for m in models_all if media_capable(m)}

def ts(t):
    return datetime.fromtimestamp(t, tz=timezone.utc).strftime("%Y-%m-%d") if t else None

records, endpoints_out = [], []
for m in models_all:
    mid = m["id"]
    kind = kind_of(m)
    sp = set(m.get("supported_parameters") or []) if isinstance(m.get("supported_parameters"), list) else set()
    rs = m.get("reasoning")
    tp = m.get("top_provider") or {}
    ep = ep_files.get(ep_key(mid))
    eps = (ep["data"].get("endpoints") if ep else None)
    prov_names = sorted({e["provider_name"] for e in eps}) if eps is not None else None
    ups = [e.get("uptime_last_1d") for e in (eps or []) if e.get("uptime_last_1d") is not None]
    pol = C.Counter(policy_of(mid, e["tag"]) for e in (eps or []))
    rec = {
        "id": mid,
        "canonical_slug": m.get("canonical_slug"),
        "name": m.get("name"),
        "author": mid.lstrip("~").split("/")[0],
        "created": ts(m.get("created")),
        "hugging_face_id": m.get("hugging_face_id") or None,
        "variant_free": mid.endswith(":free"),
        "alias_target": m.get("alias_target"),
        "expiration_date": m.get("expiration_date"),
        "knowledge_cutoff": m.get("knowledge_cutoff"),
        "kind": kind,
        "input_modalities": m["architecture"]["input_modalities"],
        "output_modalities": m["architecture"]["output_modalities"],
        "api_routes": m.get("endpoints"),
        "context_length": m.get("context_length") or None,
        "max_output_tokens": tp.get("max_completion_tokens"),
        "is_moderated": tp.get("is_moderated"),
        "caps": {
            "tools": "tools" in sp,
            "tool_choice": "tool_choice" in sp,
            "parallel_tool_calls": "parallel_tool_calls" in sp,
            "structured_outputs": "structured_outputs" in sp,
            "json_mode": "response_format" in sp,
            "reasoning": ("reasoning" in sp) or bool(rs),
            "reasoning_mandatory": (rs or {}).get("mandatory") if rs else None,
            "reasoning_efforts": (rs or {}).get("supported_efforts") if rs else None,
            "web_search_options": "web_search_options" in sp,
            "logprobs": "logprobs" in sp,
            "batch_api": "/api/v1/batches" in (m.get("endpoints") or []),
        },
        "supported_parameters": (sorted(sp) if sp else (m.get("supported_parameters") if isinstance(m.get("supported_parameters"), dict) else None)),
        "supported_voices": m.get("supported_voices"),
        "pricing": pricing_block(m, kind),
        "providers": None if eps is None else {
            "count": len(prov_names), "endpoint_count": len(eps), "names": prov_names,
            "uptime_1d_best": max(ups) if ups else None,
            "uptime_1d_worst": min(ups) if ups else None,
            "endpoints_status_nonzero": sum(1 for e in eps if e.get("status") not in (0, None)),
            "data_policy_counts": dict(pol),
            "has_zdr_endpoint": pol.get("zdr", 0) > 0,
            "endpoint_input_per_m_min": min((x for x in (per_m((e.get("pricing") or {}).get("prompt")) for e in eps) if x is not None), default=None),
            "endpoint_input_per_m_max": max((x for x in (per_m((e.get("pricing") or {}).get("prompt")) for e in eps) if x is not None), default=None),
            "endpoint_output_per_m_min": min((x for x in (per_m((e.get("pricing") or {}).get("completion")) for e in eps) if x is not None), default=None),
            "endpoint_output_per_m_max": max((x for x in (per_m((e.get("pricing") or {}).get("completion")) for e in eps) if x is not None), default=None),
            "service_tiers": sorted({e.get("service_tier") for e in eps if e.get("service_tier")}) or None,
            "src": "or_endpoints", "as_of": ep["fetched_at"],
        },
        "usage_rank_top_weekly": rank_pos.get(mid),
        "in_uptime_representative_set": mid in representative,
        "src": "or_models", "as_of": FETCHED,
    }
    if kind == "video" and mid in videos_cat:
        v = videos_cat[mid]
        rec["video_gen"] = {k: v.get(k) for k in ("supported_resolutions", "supported_aspect_ratios", "supported_durations",
                                                   "supported_frame_images", "generate_audio", "seed")}
        rec["video_gen"]["src"] = "or_videos_models"
    if mid in images_cat:
        rec["image_gen_params"] = {"supported_parameters": images_cat[mid].get("supported_parameters"),
                                   "supports_streaming": images_cat[mid].get("supports_streaming"),
                                   "src": "or_images_models"}
    records.append(rec)
    for e in (eps or []):
        pr = e.get("pricing") or {}
        endpoints_out.append({
            "model_id": mid, "provider": e.get("provider_name"), "tag": e.get("tag"),
            "service_tier": e.get("service_tier"), "quantization": e.get("quantization"),
            "context_length": e.get("context_length"), "max_completion_tokens": e.get("max_completion_tokens"),
            "max_prompt_tokens": e.get("max_prompt_tokens"),
            "input_per_m": per_m(pr.get("prompt")), "output_per_m": per_m(pr.get("completion")),
            "cache_read_per_m": per_m(pr.get("input_cache_read")),
            "discount": pr.get("discount"),
            "status": e.get("status"),
            "uptime_last_5m": e.get("uptime_last_5m"), "uptime_last_30m": e.get("uptime_last_30m"),
            "uptime_last_1d": e.get("uptime_last_1d"),
            "supports_tools": "tools" in (e.get("supported_parameters") or []),
            "supports_structured_outputs": "structured_outputs" in (e.get("supported_parameters") or []),
            "supports_implicit_caching": e.get("supports_implicit_caching"),
            "native_tools": sorted((e.get("native_tools") or {}).keys()) or None,
            "data_policy": policy_of(mid, e.get("tag")),
            "representative": mid in representative,
            "src": "or_endpoints+or_endpoints_zdr", "as_of": ep["fetched_at"],
        })

missing_eps = [m["id"] for m in models_all if ep_key(m["id"]) not in ep_files]

def write(name, obj):
    s = json.dumps(obj, indent=1, ensure_ascii=False)
    low = s.lower()
    for bad in ("benchmark", "artificial_analysis", "artificial analysis", "design_arena", "design arena", "intelligence_index"):
        assert bad not in low, (name, bad)
    open(f"{OUT}/{name}", "w").write(s + "\n")

meta = lambda srcs, extra=None: {"generated_at": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
                                 "as_of": AS_OF, "sources": {k: SOURCES[k] for k in srcs},
                                 "third_party_scores_removed": "OpenRouter's third-party evaluation-score object was dropped from every response before processing (see README).",
                                 **(extra or {})}

write("models.json", {"meta": meta(["or_models", "or_models_topweekly", "or_endpoints", "or_endpoints_zdr",
                                      "or_images_models", "or_videos_models"],
                                     {"count": len(records), "models_without_endpoint_fetch": missing_eps}),
                      "models": records})

# ---- endpoints-uptime.json
write("endpoints-uptime.json", {
    "meta": meta(["or_endpoints", "or_endpoints_zdr", "or_docs"], {
        "count": len(endpoints_out),
        "representative_set_rule": "top 60 by OpenRouter sort=top-weekly + every model with audio/video input or non-text output",
        "representative_model_count": len(representative),
        "uptime_definition": ("OpenRouter API reference: share of minutes in which at least 80% of provider attempts "
                              "succeeded, counting only minutes with 10 or more attempts; rate-limited and caller-caused "
                              "failures excluded; null when no minute had enough traffic."),
        "data_policy_values": {
            "zdr": "listed by /endpoints/zdr with safety_retention=false: no routine storage after processing; not trained on",
            "safety_retention": "listed by /endpoints/zdr with safety_retention=true: may retain any request only to prevent abuse; not trained on",
            "not_in_zdr_list": ("absent from /endpoints/zdr: the endpoint keeps data beyond ZDR/Safety Retention. OpenRouter's "
                                "third tier is 'Retention and Training' (data MAY be stored and used for training under the "
                                "provider's policy), but the public API exposes no per-endpoint training flag. First-party "
                                "Anthropic and OpenAI endpoints are all in this bucket, so absence does NOT mean the provider "
                                "trains. To exclude training providers at request time use provider.data_collection='deny'."),
        },
        "status_codes_note": "API enum 0,-1,-2,-3,-5,-10; only 0 appears on healthy endpoints; the docs do not define the negative codes.",
        "latency_throughput": "latency_last_30m / throughput_last_30m are null without an API key and were not collected.",
    }),
    "endpoints": endpoints_out,
})

# ---- usage rank (only with the Data API rankings file)
rank_rows = []
for slug, tok in weekly_tokens.most_common():
    rank_rows.append({"model_permaslug": slug, "model_id": slug_to_id.get(slug), "total_tokens_week": tok})
if rank: write("usage-week.json", {
    "meta": meta(["or_rankings_week", "or_models_topweekly"], {
        "citation": f"Source: OpenRouter (openrouter.ai/rankings), as of {rank['meta']['as_of']}.",
        "window": [rank["meta"]["start_date"], rank["meta"]["end_date"]],
        "note": "Token counts use each provider's own tokenizer, so rows from different providers are not directly comparable (OpenRouter docs). 'other' sums every model outside the top 50.",
    }),
    "rankings_week": rank_rows,
    "top_weekly_order_first_100": topweekly[:100],
})

# ---- models.dev diff
mdo = (mdev.get("openrouter") or {}).get("models") or {}
diffs, matched, only_or, only_md = [], 0, [], []
or_by_id = {r["id"]: r for r in records}
def close(a, b, tol=0.005):
    if a is None or b is None:
        return a == b
    return abs(a - b) <= max(tol, 0.01 * max(abs(a), abs(b)))
MOD = {"file": "pdf"}
for mid, md in mdo.items():
    r = or_by_id.get(mid)
    if not r:
        only_md.append(mid); continue
    matched += 1
    d = {}
    pr = r["pricing"]; cost = md.get("cost") or {}
    for k_or, k_md in (("input_per_m", "input"), ("output_per_m", "output"), ("cache_read_per_m", "cache_read"),
                       ("cache_write_per_m", "cache_write")):
        a, b = pr.get(k_or), cost.get(k_md)
        if (a is not None or b is not None) and not close(a, b):
            d[k_md + "_price_per_m"] = {"openrouter": a, "models_dev": b}
    lim = md.get("limit") or {}
    if lim.get("context") and r["context_length"] and lim["context"] != r["context_length"]:
        d["context"] = {"openrouter": r["context_length"], "models_dev": lim["context"]}
    if lim.get("output") and r["max_output_tokens"] and lim["output"] != r["max_output_tokens"]:
        d["max_output"] = {"openrouter": r["max_output_tokens"], "models_dev": lim["output"]}
    for cap_or, cap_md in (("tools", "tool_call"), ("structured_outputs", "structured_output"), ("reasoning", "reasoning")):
        if md.get(cap_md) is not None and bool(md[cap_md]) != bool(r["caps"][cap_or]):
            d[cap_md] = {"openrouter": r["caps"][cap_or], "models_dev": md[cap_md]}
    mi = sorted(set(MOD.get(x, x) for x in r["input_modalities"]))
    mm = sorted(set((md.get("modalities") or {}).get("input") or []))
    if mm and mi != mm:
        d["input_modalities"] = {"openrouter": mi, "models_dev": mm}
    mo = sorted(r["output_modalities"]); mmo = sorted((md.get("modalities") or {}).get("output") or [])
    if mmo and mo != mmo:
        d["output_modalities"] = {"openrouter": mo, "models_dev": mmo}
    if d:
        diffs.append({"id": mid, "diff": d})
only_or = sorted(set(or_by_id) - set(mdo))
field_counts = C.Counter(k for x in diffs for k in x["diff"])

# Claude: OpenRouter vs models.dev "anthropic" (Anthropic direct list prices)
mda = (mdev.get("anthropic") or {}).get("models") or {}
claude_rows = []
for r in records:
    if r["author"] != "anthropic":
        continue
    key = r["id"].split("/")[1].replace(".", "-")
    md = mda.get(key)
    cost = (md or {}).get("cost") or {}
    claude_rows.append({
        "id": r["id"], "models_dev_anthropic_key": key if md else None,
        "openrouter_input_per_m": r["pricing"].get("input_per_m"), "openrouter_output_per_m": r["pricing"].get("output_per_m"),
        "anthropic_direct_input_per_m": cost.get("input"), "anthropic_direct_output_per_m": cost.get("output"),
        "openrouter_context": r["context_length"], "models_dev_context": (md or {}).get("limit", {}).get("context"),
        "openrouter_max_output": r["max_output_tokens"], "models_dev_max_output": (md or {}).get("limit", {}).get("output"),
    })
write("models-dev-diff.json", {
    "meta": meta(["or_models", "models_dev"], {
        "method": ("models.dev provider 'openrouter' matched to OpenRouter /models by id. Prices compared per 1M tokens "
                   "with tolerance max($0.005, 1%). Modalities: OpenRouter 'file' treated as models.dev 'pdf'. "
                   "Neither side is assumed correct; OpenRouter's live API is the primary source for routing."),
        "matched": matched, "only_in_openrouter": len(only_or), "only_in_models_dev": len(only_md),
        "models_with_any_diff": len(diffs), "diff_field_counts": dict(field_counts),
    }),
    "only_in_models_dev": sorted(only_md),
    "only_in_openrouter": only_or,
    "diffs": diffs,
    "claude_openrouter_vs_anthropic_direct": claude_rows,
})

print("records", len(records), "endpoints", len(endpoints_out), "missing eps", len(missing_eps),
      "matched", matched, "diffs", len(diffs), dict(field_counts))
