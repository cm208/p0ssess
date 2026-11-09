"""Download OpenRouter's public model metadata (and models.dev) into <raw_dir>.

Usage: python3 -I fetch_openrouter.py <raw_dir> [--limit N]

No key is needed for models, endpoints, ZDR, image and video catalogs, or models.dev.
If OPENROUTER_API_KEY is set, the weekly rankings dataset (Data API, CC BY 4.0) is fetched too.
Every `benchmarks` object is removed from OpenRouter responses before anything is written:
it carries third-party scores whose licenses do not allow reuse.
"""
import json, os, sys, time, urllib.parse, urllib.request
from datetime import datetime, timedelta, timezone

BASE = "https://openrouter.ai"
UA = "p0ssess-registry/0.1"


def get(url, key=None, tries=3):
    headers = {"User-Agent": UA}
    if key:
        headers["Authorization"] = "Bearer " + key
    for attempt in range(tries):
        try:
            with urllib.request.urlopen(urllib.request.Request(url, headers=headers), timeout=60) as r:
                return json.load(r)
        except Exception as e:
            if attempt == tries - 1:
                raise
            print(f"retry {url}: {e}", file=sys.stderr)
            time.sleep(5 * (attempt + 1))


def strip_benchmarks(o):
    if isinstance(o, dict):
        return {k: strip_benchmarks(v) for k, v in o.items() if k != "benchmarks"}
    if isinstance(o, list):
        return [strip_benchmarks(v) for v in o]
    return o


def save(path, obj):
    with open(path, "w") as f:
        json.dump(obj, f)


def ep_name(mid):
    return mid.replace("/", "__").replace(":", "~") + ".json"


def main():
    args = sys.argv[1:]
    if not args:
        sys.exit(__doc__)
    raw = args[0]
    limit = int(args[args.index("--limit") + 1]) if "--limit" in args else None
    os.makedirs(f"{raw}/endpoints", exist_ok=True)

    fetched_at = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    lists = {
        "models_all.json": "/api/v1/models?output_modalities=all",
        "models_all_topweekly.json": "/api/v1/models?output_modalities=all&sort=top-weekly",
        "images_models.json": "/api/v1/images/models",
        "videos_models.json": "/api/v1/videos/models",
        "endpoints_zdr.json": "/api/v1/endpoints/zdr",
    }
    for fn, path in lists.items():
        save(f"{raw}/{fn}", strip_benchmarks(get(BASE + path)))
        print("ok", fn)
        time.sleep(1)
    save(f"{raw}/modelsdev.json", get("https://models.dev/api.json"))
    print("ok modelsdev.json")
    with open(f"{raw}/fetched_at.txt", "w") as f:
        f.write(fetched_at + "\n")

    models = json.load(open(f"{raw}/models_all.json"))["data"]
    done = 0
    for m in models:
        if limit is not None and done >= limit:
            break
        fn = os.path.join(raw, "endpoints", ep_name(m["id"]))
        if os.path.exists(fn):
            continue
        path = (m.get("links") or {}).get("details") or f"/api/v1/models/{m['id']}/endpoints"
        url = BASE + urllib.parse.quote(path, safe="/:-._~")
        try:
            body = strip_benchmarks(get(url))
        except Exception as e:
            print(f"failed {m['id']}: {e}", file=sys.stderr)
            continue
        body["_fetched_at"] = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
        body["_url"] = url
        save(fn, body)
        done += 1
        time.sleep(1)
    print("endpoints fetched", done)

    key = os.environ.get("OPENROUTER_API_KEY")
    if not key:
        print("OPENROUTER_API_KEY not set: skipping the rankings dataset")
        return
    end = datetime.now(timezone.utc).date() - timedelta(days=1)
    start = end - timedelta(days=6)
    q = urllib.parse.urlencode({"start_date": start.isoformat(), "end_date": end.isoformat(), "period": "week"})
    save(f"{raw}/rankings_week.json", get(f"{BASE}/api/v1/datasets/rankings-daily?{q}", key=key))
    print("ok rankings_week.json")


if __name__ == "__main__":
    main()
