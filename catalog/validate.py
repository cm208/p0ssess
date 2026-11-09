"""Validate catalog/usecases.yaml against catalog/schema.json and the registry snapshot.

Usage: python3 catalog/validate.py   (needs PyYAML and jsonschema)
"""
import json, os, sys
import yaml
import jsonschema

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)

doc = yaml.safe_load(open(os.path.join(HERE, "usecases.yaml")))
schema = json.load(open(os.path.join(HERE, "schema.json")))
errors = [f"{'/'.join(map(str, e.absolute_path))}: {e.message}"
          for e in jsonschema.Draft202012Validator(schema).iter_errors(doc)]

ucs = doc.get("use_cases", [])
ids = [u["id"] for u in ucs]
if len(ids) != len(set(ids)):
    errors.append("duplicate ids")
if [u["n"] for u in ucs] != list(range(1, len(ucs) + 1)):
    errors.append("n is not 1..N in order")
for u in ucs:
    for r in u.get("related", []):
        if r not in ids:
            errors.append(f"{u['id']}: related id {r} not in catalog")
    rng = u.get("stakes_range")
    if rng and (rng[0] > rng[1] or not rng[0] <= u["stakes"] <= rng[1]):
        errors.append(f"{u['id']}: stakes outside stakes_range")

reg_path = os.path.join(ROOT, "registry", "models.json")
if os.path.exists(reg_path):
    reg = {m["id"] for m in json.load(open(reg_path))["models"]}
    for u in ucs:
        refs = [u["recommended"]] + u["alternatives"] + u.get("ensemble", []) + [p for p in u.get("pipeline", []) if "/" in p]
        for m in refs:
            if m and m not in reg:
                errors.append(f"{u['id']}: {m} missing from registry/models.json")

for e in errors:
    print("ERROR", e)
print(f"{len(ucs)} use cases, {len(errors)} errors")
sys.exit(1 if errors else 0)
