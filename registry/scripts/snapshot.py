"""Build the small published registry snapshot from a full local build.

Usage: python3 -I snapshot.py <registry_build_dir> <benchmarks_build_dir> <catalog/usecases.yaml> <out_dir>

Keeps only the models the catalog references (recommended, alternatives, ensemble, pipeline),
with routing fields and an endpoint summary, plus benchmark scores for those models from the
allowed sources. Python stdlib only; PyYAML is used if installed.
"""
import json, os, re, sys
from datetime import datetime, timezone

REG, BENCH, CATALOG, OUT = sys.argv[1:5]
ID_RE = re.compile(r"^[a-z0-9][a-z0-9-]*/[a-z0-9][a-z0-9.\-]*(:[a-z]+)?$")
FORBIDDEN = ("artificial analysis", "artificial_analysis", "artificial-analysis", "design arena", "design_arena",
             "design-arena", "swe-bench", "swe_bench", "livecodebench", "mmmu", "vellum", "intelligence_index")


def catalog_refs(path):
    """Return {model_id: [use_case_id, ...]}."""
    try:
        import yaml
        ucs = yaml.safe_load(open(path))["use_cases"]
        pairs = []
        for u in ucs:
            for m in [u.get("recommended")] + u.get("alternatives", []) + u.get("ensemble", []) + u.get("pipeline", []):
                pairs.append((u["id"], m))
    except ImportError:
        pairs, cur, mode = [], None, None
        for line in open(path):
            line = line.rstrip("\n")
            if re.match(r"^  id: ", line):
                cur = line.split(": ", 1)[1].strip("'\"")
            m = re.match(r"^  recommended: (.+)$", line)
            if m:
                pairs.append((cur, m.group(1).strip("'\"")))
            if re.match(r"^  (alternatives|ensemble|pipeline):\s*$", line):
                mode = True
                continue
            m = re.match(r"^  - (.+)$", line)
            if mode and m:
                pairs.append((cur, m.group(1).strip("'\"")))
            else:
                mode = None
    refs = {}
    for uc, m in pairs:
        if m and ID_RE.match(m):
            refs.setdefault(m, [])
            if uc not in refs[m]:
                refs[m].append(uc)
    return refs


def blend(e):
    if e.get("input_per_m") is None or e.get("output_per_m") is None:
        return None
    return (3 * e["input_per_m"] + e["output_per_m"]) / 4


def ep_brief(e):
    return {k: e.get(k) for k in ("provider", "tag", "service_tier", "input_per_m", "output_per_m",
                                  "cache_read_per_m", "context_length", "max_completion_tokens",
                                  "data_policy", "uptime_last_1d")}


def cheapest(rows):
    priced = [e for e in rows if blend(e) is not None]
    return ep_brief(min(priced, key=blend)) if priced else None


def check(name, obj):
    low = json.dumps(obj).lower()
    for bad in FORBIDDEN:
        assert bad not in low, (name, bad)


def write(name, obj):
    check(name, obj.get("records", obj.get("models")))
    os.makedirs(os.path.dirname(os.path.join(OUT, name)), exist_ok=True)
    with open(os.path.join(OUT, name), "w") as f:
        if isinstance(obj, dict) and "records" in obj:
            head = {k: v for k, v in obj.items() if k != "records"}
            f.write(json.dumps(head, indent=1, ensure_ascii=False)[:-2] + ',\n "records": [\n')
            f.write(",\n".join("  " + json.dumps(r, ensure_ascii=False) for r in obj["records"]))
            f.write("\n ]\n}\n")
        else:
            f.write(json.dumps(obj, indent=1, ensure_ascii=False) + "\n")


refs = catalog_refs(CATALOG)
models = {m["id"]: m for m in json.load(open(os.path.join(REG, "models.json")))["models"]}
reg_meta = json.load(open(os.path.join(REG, "models.json")))["meta"]
eps = json.load(open(os.path.join(REG, "endpoints-uptime.json")))
missing = sorted(set(refs) - set(models))
assert not missing, f"catalog references models absent from the registry build: {missing}"

by_model = {}
for e in eps["endpoints"]:
    by_model.setdefault(e["model_id"], []).append(e)

CAPS = ("tools", "tool_choice", "parallel_tool_calls", "structured_outputs", "json_mode", "reasoning",
        "reasoning_mandatory", "reasoning_efforts", "web_search_options", "batch_api")
POLICY_RANK = {"zdr": 0, "safety_retention": 1, "not_in_zdr_list": 2}
out_models = []
for mid in sorted(refs):
    m = models[mid]
    rows = by_model.get(mid, [])
    pv = m.get("providers") or {}
    pol = [e["data_policy"] for e in rows]
    rec = {
        "id": mid, "name": m["name"], "author": m["author"], "kind": m["kind"],
        "hugging_face_id": m.get("hugging_face_id"),
        "input_modalities": m["input_modalities"], "output_modalities": m["output_modalities"],
        "api_routes": m.get("api_routes"),
        "context_length": m.get("context_length"), "max_output_tokens": m.get("max_output_tokens"),
        "caps": {k: m["caps"].get(k) for k in CAPS},
        "pricing": m["pricing"],
        "endpoints": {
            "provider_count": pv.get("count"), "endpoint_count": pv.get("endpoint_count"),
            "providers": pv.get("names"),
            "uptime_1d_best": pv.get("uptime_1d_best"), "uptime_1d_worst": pv.get("uptime_1d_worst"),
            "data_policy_counts": pv.get("data_policy_counts"),
            "best_data_policy": min(pol, key=POLICY_RANK.get) if pol else None,
            "has_zdr_endpoint": pv.get("has_zdr_endpoint"),
            "service_tiers": pv.get("service_tiers"),
            "cheapest": cheapest(rows),
            "cheapest_zdr": cheapest([e for e in rows if e["data_policy"] == "zdr"]),
            "cheapest_rule": "lowest (3 x input + output) / 4 per 1M tokens among endpoints with token prices",
            "src": "or_endpoints+or_endpoints_zdr", "as_of": pv.get("as_of"),
        },
        "referenced_by": refs[mid],
        "src": "or_models", "as_of": m.get("as_of"),
    }
    out_models.append(rec)

generated = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
srcs = {k: v for k, v in reg_meta["sources"].items()
        if k in ("or_models", "or_endpoints", "or_endpoints_zdr", "or_images_models", "or_videos_models")}
write("models.json", {
    "meta": {"generated_at": generated, "as_of": reg_meta.get("as_of"), "count": len(out_models),
             "scope": "Only models referenced by catalog/usecases.yaml. Rebuild the full registry locally with the scripts in registry/scripts.",
             "sources": srcs, "third_party_scores_removed": reg_meta.get("third_party_scores_removed")},
    "models": out_models,
})

ids = set(refs)
summary = {}


def subset(fname, keep, also_excluded=None):
    path = os.path.join(BENCH, fname)
    if not os.path.exists(path):
        return
    d = json.load(open(path))
    recs = [r for r in d["records"] if r.get("openrouter_id") in ids and keep(r)]
    head = {k: v for k, v in d.items() if k != "records"}
    head["scope"] = "Records for models referenced by catalog/usecases.yaml only."
    head["count"] = len(recs)
    if also_excluded:
        ex = head.get("excluded") or []
        head["excluded"] = (ex if isinstance(ex, list) else [ex]) + also_excluded
    write(os.path.join("benchmarks", fname), {**head, "records": recs})
    summary[fname] = {"records": len(recs), "models": len({r["openrouter_id"] for r in recs})}


subset("lmarena.json", lambda r: True)
subset("epoch_ai.json", lambda r: r.get("listed_as_epoch_run_on_methodology_page") and not r.get("superseded_by"),
       ["internal runs that Epoch's methodology page does not list as Epoch-run",
        "superseded FrontierMath versions"])
subset("openrouter_evals.json", lambda r: True)
subset("aider_polyglot.json", lambda r: True)
print(json.dumps({"models": len(out_models), "benchmarks": summary}))
