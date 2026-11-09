"""Download the cleanly licensed benchmark sources into <dl_dir>.

Usage: python3 -I fetch_benchmarks.py <dl_dir> [--subsets a,b,...]

- Epoch AI benchmark_data.zip (CC BY 4.0): only the benchmarks Epoch runs itself and lists on
  its methodology page (GPQA Diamond, MATH Level 5, OTIS Mock AIME, FrontierMath) plus the two
  metadata files. External results and every other file are never extracted.
- LMArena leaderboard dataset (CC BY 4.0), `latest` split, through the Hugging Face rows API.
  --subsets limits which subsets are fetched (for testing).
- Aider polyglot leaderboard YAML (Apache-2.0).
- With OPENROUTER_API_KEY set: OpenRouter's own evals (rows with source=openrouter only;
  everything else is discarded before writing) and the 30-day model rankings (CC BY 4.0).
"""
import io, json, os, sys, time, urllib.parse, urllib.request, zipfile
from datetime import datetime, timedelta, timezone

UA = "p0ssess-registry/0.1"
LM_DATASET = "lmarena-ai/leaderboard-dataset"
LM_SUBSETS = [
    "text_style_control", "text_factuality", "vision_style_control", "document_style_control",
    "search_style_control", "search_factuality", "webdev", "agent", "agent_bash_reliability",
    "agent_praise_complaint", "agent_steerability", "agent_task_outcome_explicit",
    "agent_tool_hallucination", "text_to_image", "image_edit", "text_to_video", "image_to_video",
    "video_edit",
]
EPOCH_ALLOWED = {
    "gpqa_diamond.csv", "math_level_5.csv", "otis_mock_aime_2024_2025.csv", "frontiermath.csv",
    "frontiermath_tier_4.csv", "frontiermath_tiers_1_3_v2.csv", "frontiermath_tier_4_v2.csv",
    "model_metadata.csv", "benchmark_metadata.csv",
}


def fetch(url, key=None, tries=4):
    headers = {"User-Agent": UA}
    if key:
        headers["Authorization"] = "Bearer " + key
    for attempt in range(tries):
        try:
            with urllib.request.urlopen(urllib.request.Request(url, headers=headers), timeout=120) as r:
                return r.read()
        except Exception as e:
            if attempt == tries - 1:
                raise
            print(f"retry {url}: {e}", file=sys.stderr)
            time.sleep(5 * (attempt + 1))


def get_json(url, key=None):
    return json.loads(fetch(url, key))


def save_json(path, obj):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        json.dump(obj, f)


def epoch(dl):
    os.makedirs(f"{dl}/epoch", exist_ok=True)
    z = zipfile.ZipFile(io.BytesIO(fetch("https://epoch.ai/data/benchmark_data.zip")))
    names = {os.path.basename(n): n for n in z.namelist()}
    for fn in sorted(EPOCH_ALLOWED):
        if fn not in names:
            print(f"epoch: {fn} not in the zip", file=sys.stderr)
            continue
        with open(f"{dl}/epoch/{fn}", "wb") as f:
            f.write(z.read(names[fn]))
    print("ok epoch", len(os.listdir(f"{dl}/epoch")), "files")


def lmarena(dl, subsets):
    info = get_json(f"https://huggingface.co/api/datasets/{LM_DATASET}")
    save_json(f"{dl}/lmarena/revision.json", {"sha": info.get("sha"), "last_modified": info.get("lastModified")})
    for sub in subsets:
        rows, offset, total = [], 0, None
        while total is None or offset < total:
            q = urllib.parse.urlencode({"dataset": LM_DATASET, "config": sub, "split": "latest",
                                        "offset": offset, "length": 100})
            page = get_json(f"https://datasets-server.huggingface.co/rows?{q}")
            total = page["num_rows_total"]
            rows += [r["row"] for r in page["rows"]]
            if not page["rows"]:
                break
            offset += len(page["rows"])
            time.sleep(1)
        save_json(f"{dl}/lmarena/{sub}.json", {"subset": sub, "split": "latest", "rows": rows})
        print("ok lmarena", sub, len(rows))


def aider(dl):
    os.makedirs(f"{dl}/aider", exist_ok=True)
    url = "https://raw.githubusercontent.com/Aider-AI/aider/main/aider/website/_data/polyglot_leaderboard.yml"
    with open(f"{dl}/aider/polyglot_leaderboard.yml", "wb") as f:
        f.write(fetch(url))
    print("ok aider")


def openrouter(dl, key):
    body = get_json("https://openrouter.ai/api/v1/benchmarks?source=openrouter", key)
    body["data"] = [r for r in body.get("data", []) if r.get("source") == "openrouter"]
    save_json(f"{dl}/or/benchmarks_openrouter_only.json", {"meta": body.get("meta"), "data": body["data"]})
    print("ok openrouter evals", len(body["data"]))
    end = datetime.now(timezone.utc).date() - timedelta(days=1)
    q = urllib.parse.urlencode({"start_date": (end - timedelta(days=29)).isoformat(), "end_date": end.isoformat()})
    save_json(f"{dl}/or/rankings_daily.json", get_json(f"https://openrouter.ai/api/v1/datasets/rankings-daily?{q}", key))
    print("ok openrouter rankings")


def main():
    args = sys.argv[1:]
    if not args:
        sys.exit(__doc__)
    dl = args[0]
    subsets = args[args.index("--subsets") + 1].split(",") if "--subsets" in args else LM_SUBSETS
    unknown = set(subsets) - set(LM_SUBSETS)
    if unknown:
        sys.exit(f"unknown LMArena subsets: {sorted(unknown)}")
    os.makedirs(dl, exist_ok=True)
    with open(f"{dl}/retrieved.txt", "w") as f:
        f.write(datetime.now(timezone.utc).strftime("%Y-%m-%d") + "\n")
    epoch(dl)
    lmarena(dl, subsets)
    aider(dl)
    key = os.environ.get("OPENROUTER_API_KEY")
    if key:
        openrouter(dl, key)
    else:
        print("OPENROUTER_API_KEY not set: skipping OpenRouter evals and rankings")


if __name__ == "__main__":
    main()
