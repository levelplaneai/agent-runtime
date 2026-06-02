#!/usr/bin/env python3
"""
run_eda.py — Engineering Drawing Analysis runner.

Registers all tool implementations as plain Python functions and executes the
engineering_drawing_analysis.agent bundle via the agent-runtime SDK.

Usage:
    python run_eda.py <drawing_path> [<output_dir>]

The agent-runtime binary is resolved from the AGENT_RUNTIME_BIN env var,
then from ./bin/agent-runtime, then from PATH.
"""
from __future__ import annotations

import json
import os
import re
import sys
import time
from collections import defaultdict
from pathlib import Path

# ── SDK import ────────────────────────────────────────────────────────────────
from agent_runtime import FileInput, Runtime, RunError, TraceEvent

# Point at the binary we just built if no override is set.
_HERE = Path(__file__).parent
os.environ.setdefault("AGENT_RUNTIME_BIN", str(_HERE / "bin" / "agent-runtime"))

BUNDLE = str(_HERE / "engineering_drawing_analysis.agent")

# ── lazy dependency bootstrap (PIL + pdf2image) ────────────────────────────────
def _ensure(*packages: str) -> None:
    import importlib.util, subprocess
    missing = [p for p in packages if importlib.util.find_spec(p.replace("-", "_").split(".")[0]) is None]
    if missing:
        subprocess.check_call([sys.executable, "-m", "pip", "install", "-q", *missing], stdout=subprocess.DEVNULL)

_ensure("Pillow", "pdf2image")

# ── shared helpers ────────────────────────────────────────────────────────────

def _safe(name: str) -> str:
    return re.sub(r"[^a-zA-Z0-9_-]", "_", name).strip("_")[:80]


def _load_drawing(path: str):
    from PIL import Image
    from pdf2image import convert_from_path
    p = Path(path)
    if p.suffix.lower() == ".pdf":
        pages = convert_from_path(str(p), dpi=150, first_page=1, last_page=1)
        return pages[0].convert("RGB")
    return Image.open(path).convert("RGB")


def _crop(img, ymin, xmin, ymax, xmax):
    w, h = img.size
    l = max(0, int(xmin / 1000 * w))
    r = max(l + 1, min(int(xmax / 1000 * w), w))
    t = max(0, int(ymin / 1000 * h))
    b = max(t + 1, min(int(ymax / 1000 * h), h))
    return img.crop((l, t, r, b))


_NON_GEO = ("titleblock", "title", "revision", "note",
            "technicalrequirement", "partslist", "billofmaterial", "legend")

def _is_non_geo(name: str) -> bool:
    n = re.sub(r"[^a-z0-9]", "", name.lower())
    return any(kw in n for kw in _NON_GEO)


# ── Runtime + tools ───────────────────────────────────────────────────────────

rt = Runtime()


@rt.tool("image.crop_views", version="v1")
def crop_views(drawing_path: str, view_boxes: str, output_dir: str) -> dict:
    """Load the drawing and save one PNG per view bounding box."""
    views_data = json.loads(view_boxes)
    img = _load_drawing(drawing_path)
    out = Path(output_dir) / "crops" / "views"
    out.mkdir(parents=True, exist_ok=True)
    result = []
    for v in views_data:
        crop = _crop(img, v["ymin"], v["xmin"], v["ymax"], v["xmax"])
        path = str(out / f"{_safe(v['viewName'])}.png")
        crop.save(path, "PNG")
        result.append({"name": v["viewName"], "path": path})
    return {"views": result}


@rt.tool("image.crop_features", version="v1")
def crop_features(views: str, phase3_results: str, output_dir: str) -> dict:
    """Crop atomic feature bounding boxes from each view's crop image."""
    from PIL import Image
    views_list    = json.loads(views)
    p3_results    = json.loads(phase3_results)
    out           = Path(output_dir) / "crops" / "features"
    out.mkdir(parents=True, exist_ok=True)

    phase4_input  = []
    all_features  = []
    non_geo_views = []

    for view_info, raw_result in zip(views_list, p3_results):
        vname = view_info["name"]
        vpath = view_info["path"]
        if _is_non_geo(vname):
            non_geo_views.append(vname)
            continue

        view_img = Image.open(vpath).convert("RGB")
        feats    = raw_result.get("features", raw_result) if isinstance(raw_result, dict) else raw_result
        pairs    = []
        for feat in feats:
            fname = feat.get("featureName", "unknown")
            crop  = _crop(view_img, feat["ymin"], feat["xmin"], feat["ymax"], feat["xmax"])
            path  = str(out / f"{_safe(fname)}.png")
            crop.save(path, "PNG")
            pairs.append({"featureName": fname, "crop_path": path})
            all_features.append(feat)

        phase4_input.append({
            "view_name":           vname,
            "view_crop_path":      vpath,
            "features_with_crops": pairs,
            "feature_names":       [p["featureName"] for p in pairs],
        })

    return {
        "phase4_input":        phase4_input,
        "all_features_json":   json.dumps(all_features),
        "non_geometric_views": non_geo_views,
    }


@rt.tool("image.crop_dims", version="v1")
def crop_dims(views: str, phase4_results: str, output_dir: str) -> dict:
    """Crop dimension callout bounding boxes from each view's crop image."""
    from PIL import Image
    views_list   = json.loads(views)
    p4_results   = json.loads(phase4_results)
    out          = Path(output_dir) / "crops" / "dims"
    out.mkdir(parents=True, exist_ok=True)

    all_dims: list[dict]                        = []
    dim_data: dict[str, dict]                   = defaultdict(lambda: {"dims": [], "dim_crop_paths": []})

    for view_info, raw_result in zip(views_list, p4_results):
        vpath = view_info["path"]
        dims  = raw_result.get("dimensions", raw_result) if isinstance(raw_result, dict) else raw_result
        view_img = None

        for i, dim in enumerate(dims or []):
            if view_img is None:
                view_img = Image.open(vpath).convert("RGB")
            fname = dim.get("featureName", "unknown")
            slug  = _safe(f"{fname}_{i}")
            crop  = _crop(view_img, dim["ymin"], dim["xmin"], dim["ymax"], dim["xmax"])
            path  = str(out / f"{slug}.png")
            crop.save(path, "PNG")
            all_dims.append(dim)
            dim_data[fname]["dims"].append(dim)
            dim_data[fname]["dim_crop_paths"].append(path)

    return {
        "all_dims_json":      json.dumps(all_dims),
        "dim_data_by_feature": dict(dim_data),
    }


@rt.tool("drawing.prepare_feature_queue", version="v1")
def prepare_feature_queue(all_dims_json: str) -> dict:
    """Deduplicate and order the feature names from phase4 dims to seed the loop."""
    dims         = json.loads(all_dims_json)
    feature_names = list(dict.fromkeys(d["featureName"] for d in dims if d.get("featureName")))
    return {"feature_names": feature_names}


@rt.tool("drawing.verify_feature", version="v1")
def verify_feature(
    feature_name: str,
    all_features_json: str,
    all_dims_json: str,
    output_dir: str,
) -> dict:
    """
    Phase 5 — spatial verification for one feature.
    Builds the TS-mirrored interleaved multimodal payload and calls Gemini.
    Returns the verification result plus any newly discovered feature names.
    """
    _ensure("google-genai")
    from google import genai
    from google.genai import types
    from google.genai.types import GenerateContentConfig

    all_features = json.loads(all_features_json)
    all_dims     = json.loads(all_dims_json)

    # ── Gemini client ─────────────────────────────────────────────────────────
    llm_cfg  = json.loads(os.environ.get("LLM_CONFIG", "{}"))
    creds    = llm_cfg.get("credentials", {})
    provider = llm_cfg.get("document", {}).get("provider", "gemini_vertex")
    model    = llm_cfg.get("document", {}).get("model", "gemini-2.5-pro")

    http_opts = {"timeout": None}
    if "vertex" in provider:
        client = genai.Client(
            vertexai=True,
            project=creds.get("vertex_project_id", ""),
            location=creds.get("vertex_location", "us-east5"),
            http_options=http_opts,
        )
    else:
        client = genai.Client(
            api_key=creds.get("gemini_api_key", os.environ.get("GEMINI_API_KEY", "")),
            http_options=http_opts,
        )

    # ── Build interleaved payload ─────────────────────────────────────────────
    def _img_part(path: str) -> types.Part:
        return types.Part.from_bytes(data=Path(path).read_bytes(), mime_type="image/png")

    contents: list = []

    # All view crops, labeled
    views_dir = Path(output_dir) / "crops" / "views"
    if views_dir.exists():
        for png in sorted(views_dir.glob("*.png")):
            contents.append(f"View Name: {png.stem.replace('_', ' ').title()}")
            contents.append(_img_part(str(png)))

    contents.append(f"--- Target Feature: {feature_name} ---")

    # Per-dim: text labels + dim crop
    dims_dir   = Path(output_dir) / "crops" / "dims"
    feat_dims  = [d for d in all_dims if d.get("featureName") == feature_name]
    for i, dim in enumerate(feat_dims):
        contents.append(f"Dimension found: {dim.get('dimensionLxB', '')}")
        if dim.get("rawTextCallout"):
            contents.append(f"Raw callout: {dim['rawTextCallout']}")
        if dim.get("dimensionDetails"):
            contents.append(f"Details: {dim['dimensionDetails']}")
        if dims_dir.exists():
            candidate = dims_dir / f"{_safe(feature_name)}_{i}.png"
            if candidate.exists():
                contents.append("Dimension Crop:")
                contents.append(_img_part(str(candidate)))

    # Feature crop
    feats_dir = Path(output_dir) / "crops" / "features"
    if feats_dir.exists():
        feat_crop = feats_dir / f"{_safe(feature_name)}.png"
        if feat_crop.exists():
            view_ctx = next(
                (f.get("viewContext", "") for f in all_features if f.get("featureName") == feature_name), ""
            )
            contents.append(f"Feature Crop in context of: {view_ctx}")
            contents.append(_img_part(str(feat_crop)))

    # Prompt
    contents.append(
        f"""Step 6: Spatial Analysis for Envelope Identification — analyzing ONE specific feature.
You are an expert mechanical engineer. Use your vision capabilities.

For the SPECIFIC FEATURE "{feature_name}", perform SPATIAL INCORPORATION ANALYSIS:

0. EXISTS AS PHYSICAL FEATURE: set existsAsPhysicalFeature (bool).
1. MENTAL TRACE OF OUTER SKIN: does this feature form part of the absolute outer boundary?
2. GEOMETRY: SOLID MATERIAL ADDITION vs MATERIAL REMOVAL. Voids/holes do NOT increase the bounding box.
3. ENVELOPE CONTRIBUTION (impactsEnvelope: YES/NO/MAYBE) and AXES (Length/Breadth/Height).
4. CORRECTED FEATURE NAME: provide correctedFeatureName if wrong, else return "{feature_name}" unchanged.
5. MISSED GEOMETRY: list any newly discovered features in newFoundFeatures (featureName, dimensionLxB, reasoning).

Return a JSON array with EXACTLY ONE FeatureVerificationInfo object for: "{feature_name}".
"""
    )

    schema = {
        "type": "ARRAY",
        "items": {
            "type": "OBJECT",
            "properties": {
                "featureName":             {"type": "STRING"},
                "correctedFeatureName":    {"type": "STRING"},
                "existsAsPhysicalFeature": {"type": "BOOLEAN"},
                "visibleInViews": {"type": "ARRAY", "items": {
                    "type": "OBJECT",
                    "properties": {"viewName": {"type": "STRING"}, "isVisible": {"type": "BOOLEAN"}},
                    "required": ["viewName", "isVisible"],
                }},
                "impactsEnvelope": {"type": "STRING", "enum": ["YES", "NO", "MAYBE"]},
                "associatedAxes":  {"type": "ARRAY", "items": {"type": "STRING"}},
                "reasoning":       {"type": "STRING"},
                "newFoundFeatures": {"type": "ARRAY", "items": {
                    "type": "OBJECT",
                    "properties": {
                        "featureName":  {"type": "STRING"},
                        "dimensionLxB": {"type": "STRING"},
                        "reasoning":    {"type": "STRING"},
                    },
                    "required": ["featureName", "dimensionLxB", "reasoning"],
                }},
            },
            "required": ["featureName", "existsAsPhysicalFeature", "impactsEnvelope", "associatedAxes", "reasoning"],
        },
    }

    cfg = GenerateContentConfig(
        response_mime_type="application/json",
        response_schema=schema,
        temperature=0.1,
        max_output_tokens=65536,
    )

    for attempt in range(3):
        try:
            resp = client.models.generate_content(model=model, contents=contents, config=cfg)
            items = json.loads(resp.text or "[]")
            break
        except Exception as exc:
            if attempt == 2:
                raise
            time.sleep(2 ** attempt)
            print(f"  verify_feature attempt {attempt+1} failed ({exc}), retrying…", file=sys.stderr)

    vdata        = (items[0] if isinstance(items, list) and items else {})
    new_features = [nf["featureName"] for nf in (vdata.get("newFoundFeatures") or []) if nf.get("featureName")]

    return {
        "featureName":             vdata.get("featureName", feature_name),
        "correctedFeatureName":    (vdata.get("correctedFeatureName") or feature_name).strip() or feature_name,
        "existsAsPhysicalFeature": vdata.get("existsAsPhysicalFeature", True),
        "visibleInViews":          vdata.get("visibleInViews", []),
        "impactsEnvelope":         vdata.get("impactsEnvelope", "MAYBE"),
        "associatedAxes":          vdata.get("associatedAxes", ["None"]),
        "reasoning":               vdata.get("reasoning", ""),
        "new_features":            new_features,
    }


@rt.tool("drawing.build_feature_split", version="v1")
def build_feature_split(verifications: str, all_features_json: str, all_dims_json: str) -> dict:
    """Merge phase3/4/5 data and split into contributing / non-contributing subsets."""
    vers        = json.loads(verifications) if isinstance(verifications, str) else verifications
    all_features = json.loads(all_features_json)
    all_dims     = json.loads(all_dims_json)

    by_ver = {v.get("featureName"): v for v in vers}

    # Build allFeatures (non-minor crops + isNewFeature dims)
    all_built: list[dict] = []
    for c in all_features:
        if c.get("isMinor"):
            continue
        fname     = c.get("featureName")
        feat_dims = [d for d in all_dims if d.get("featureName") == fname]
        all_built.append({"featureName": fname, "dimensions": feat_dims, "vData": by_ver.get(fname), "cropDetails": c})
    for d in all_dims:
        if not d.get("isNewFeature"):
            continue
        fname = d.get("featureName")
        all_built.append({"featureName": fname, "dimensions": [d], "vData": by_ver.get(fname)})

    def _fmt(d: dict) -> str:
        return f"{d.get('dimensionLxB','')} - Callout: {d.get('rawTextCallout','')} - Details: {d.get('dimensionDetails','')}"

    contributing     = []
    non_contributing = []
    for f in all_built:
        v      = f.get("vData") or {}
        dim_s  = " | ".join(_fmt(d) for d in f.get("dimensions", []))
        exists = v.get("existsAsPhysicalFeature")
        impact = v.get("impactsEnvelope")
        entry  = {"featureName": f["featureName"], "dimensions": dim_s,
                  "reasoning": v.get("reasoning"), "associatedAxes": v.get("associatedAxes")}
        if v and exists is not False and impact in ("YES", "MAYBE"):
            contributing.append(entry)
        else:
            if not entry["reasoning"]:
                entry["reasoning"] = "Not a physical feature" if exists is False else "Unknown"
            non_contributing.append(entry)

    return {"contributing": contributing, "non_contributing": non_contributing, "all_features_built": all_built}


@rt.tool("drawing.reconciliation_graph", version="v1")
def reconciliation_graph(spatial_data: str, output_dir: str) -> dict:
    """Phase 9 — build the feature dependency graph from phase8 spatial understanding. No LLM call."""
    raw = json.loads(spatial_data) if isinstance(spatial_data, str) else spatial_data

    # Flatten nested list from map_phase8 (items is list of per-feature lists)
    flat: list[dict] = []
    for item in raw:
        if isinstance(item, list):
            flat.extend(item)
        elif isinstance(item, dict):
            sub = item.get("spatial_data") or []
            flat.extend(sub) if isinstance(sub, list) else flat.append(item)

    nodes = [
        {
            "id":                          f"rg-{i}",
            "featureName":                 su.get("featureName", ""),
            "dimensions":                  su.get("dimensions", []),
            "manufacturing_nature":        su.get("manufacturing_nature", []),
            "skin_or_internal":            su.get("skin_or_internal"),
            "physicalGeometryExplanation": su.get("physicalGeometryExplanation", ""),
        }
        for i, su in enumerate(flat)
    ]

    edge_set: set[str] = set()
    edges = []
    for src, su in enumerate(flat):
        for fc in su.get("featureCorrelation", []):
            tgt = next((i for i, s in enumerate(flat) if s.get("featureName") == fc.get("relatedFeatureName")), None)
            if tgt is None:
                continue
            key = f"{min(src,tgt)}-{max(src,tgt)}"
            if key not in edge_set:
                edge_set.add(key)
                edges.append({"source": f"rg-{src}", "target": f"rg-{tgt}", "label": fc.get("relationshipType", "")})

    graph = {"nodes": nodes, "edges": edges}
    Path(output_dir).mkdir(parents=True, exist_ok=True)
    (Path(output_dir) / "phase9.json").write_text(json.dumps(graph, indent=2))
    return graph


# ── CLI entry point ───────────────────────────────────────────────────────────

def main() -> None:
    if len(sys.argv) < 2:
        print("Usage: python run_eda.py <drawing_path> [<output_dir>]", file=sys.stderr)
        sys.exit(1)

    drawing    = Path(sys.argv[1]).resolve()
    output_dir = Path(sys.argv[2]).resolve() if len(sys.argv) > 2 else Path("/tmp/eda_output")

    if not drawing.exists():
        print(f"Drawing not found: {drawing}", file=sys.stderr)
        sys.exit(1)

    output_dir.mkdir(parents=True, exist_ok=True)
    print(f"Drawing:    {drawing}")
    print(f"Output dir: {output_dir}")
    print(f"Bundle:     {BUNDLE}")
    print()

    def on_event(ev: TraceEvent) -> None:
        if ev.event in ("node_start", "node_done"):
            dur = f" {ev.duration_ms}ms" if ev.duration_ms else ""
            print(f"  [{ev.event:<10}] {ev.node:<30} type={ev.node_type}{dur}")
        elif ev.event in ("flow_start", "flow_done"):
            print(f"[{ev.event}]{f'  {ev.duration_ms}ms' if ev.duration_ms else ''}")
        elif ev.event == "llm_request":
            print(f"  [llm →     ] {ev.node:<30} model={ev.model}")
        elif ev.event == "llm_response":
            print(f"  [llm ←     ] {ev.node:<30} in={ev.input_tokens} out={ev.output_tokens} {ev.duration_ms}ms")
        elif ev.event in ("loop_start", "loop_item_done", "loop_queue_extended"):
            print(f"  [{ev.event:<10}] {ev.node:<30} {ev.item_index}/{ev.item_count}")

    try:
        result = rt.run(
            BUNDLE,
            inputs={
                "drawing":    str(drawing),
                "output_dir": str(output_dir),
            },
            on_event=on_event,
        )
        print(f"\nDone. Output keys: {list(result.keys())}")
        print(f"Results written to: {output_dir}")
    except RunError as e:
        print(f"\nFlow error: {e} (run_id={e.run_id})", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
