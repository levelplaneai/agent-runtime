#!/usr/bin/env python3
"""
drawing.verify_feature — Tool exec script (phase 5 per-feature).

Builds the exact interleaved multimodal payload for one feature's spatial
verification, calls the Gemini model, and returns the result. Also maps
correctedFeatureName renames and surfaces newFoundFeatures so the loop node
can extend the verification queue.

stdin: {
  "feature_name":      "Bore A",
  "all_features_json": "<json string>",
  "all_dims_json":     "<json string>",
  "output_dir":        "/tmp/eda_output"
}
stdout: {
  "featureName":             "Bore A",
  "correctedFeatureName":    "Main Bore",   (or same as featureName)
  "existsAsPhysicalFeature": true,
  "impactsEnvelope":         "YES",
  "associatedAxes":          ["Length", "Breadth"],
  "reasoning":               "...",
  "new_features":            ["Discovered Feature X"]   (empty list if none)
}
"""
import sys
import json
import os
import time
from pathlib import Path

import importlib.util, subprocess as _sp

def _ensure(pkg, import_as=None):
    if importlib.util.find_spec(import_as or pkg) is None:
        _sp.check_call([sys.executable, "-m", "pip", "install", "-q", pkg], stdout=_sp.DEVNULL)

_ensure("google-genai", "google.genai")
_ensure("Pillow", "PIL")

from google import genai
from google.genai import types
from google.genai.types import GenerateContentConfig

# ─── Config ──────────────────────────────────────────────────────────────────

_llm_cfg   = json.loads(os.environ.get("LLM_CONFIG", "{}"))
_creds     = _llm_cfg.get("credentials", {})
_provider  = _llm_cfg.get("document", {}).get("provider", "gemini_vertex")
MODEL      = _llm_cfg.get("document", {}).get("model", "gemini-2.5-pro")
MAX_RETRIES = 3

# ─── Gemini client ───────────────────────────────────────────────────────────

def _client():
    http = {"timeout": None}
    if "vertex" in _provider:
        return genai.Client(
            vertexai=True,
            project=_creds.get("vertex_project_id", ""),
            location=_creds.get("vertex_location", "us-east5"),
            http_options=http,
        )
    return genai.Client(api_key=_creds.get("gemini_api_key", os.environ.get("GEMINI_API_KEY", "")), http_options=http)


def call_gemini(contents: list, schema: dict) -> dict:
    cfg = GenerateContentConfig(
        response_mime_type="application/json",
        response_schema=schema,
        temperature=0.1,
        max_output_tokens=65536,
    )
    client = _client()
    for attempt in range(MAX_RETRIES):
        try:
            resp = client.models.generate_content(model=MODEL, contents=contents, config=cfg)
            return json.loads(resp.text or "{}")
        except Exception as exc:
            if attempt == MAX_RETRIES - 1:
                raise
            time.sleep(2 ** attempt)
            print(f"  attempt {attempt+1} failed ({exc}), retrying…", file=sys.stderr)

# ─── Payload assembly ────────────────────────────────────────────────────────

def encode_image(path: str) -> types.Part:
    return types.Part.from_bytes(data=Path(path).read_bytes(), mime_type="image/png")


def _build_payload(feature_name, all_features, all_dims, output_dir):
    """Mirror the TS step6 per-feature payload:
    view_crops (labeled) + target feature header + dim info + dim crops + feature crop
    """
    parts = []

    # ── view crops (labeled) ──
    views_dir = Path(output_dir) / "crops" / "views"
    if views_dir.exists():
        for png in sorted(views_dir.glob("*.png")):
            vname = png.stem.replace("_", " ").title()
            parts.append(f"View Name: {vname}")
            parts.append(encode_image(str(png)))

    # ── target feature header ──
    parts.append(f"--- Target Feature: {feature_name} ---")

    # ── dimension entries + dim crops ──
    dims_dir = Path(output_dir) / "crops" / "dims"
    feat_dims = [d for d in all_dims if d.get("featureName") == feature_name]
    for i, dim in enumerate(feat_dims):
        parts.append(f"Dimension found: {dim.get('dimensionLxB','')}")
        if dim.get("rawTextCallout"):
            parts.append(f"Raw callout: {dim['rawTextCallout']}")
        if dim.get("dimensionDetails"):
            parts.append(f"Details: {dim['dimensionDetails']}")
        # find a matching dim crop
        if dims_dir.exists():
            slug = re.sub(r"[^a-zA-Z0-9_-]", "_", feature_name)[:80]
            candidate = dims_dir / f"{slug}_{i}.png"
            if candidate.exists():
                parts.append("Dimension Crop:")
                parts.append(encode_image(str(candidate)))

    # ── feature crop ──
    feats_dir = Path(output_dir) / "crops" / "features"
    if feats_dir.exists():
        slug = re.sub(r"[^a-zA-Z0-9_-]", "_", feature_name)[:80]
        feat_crop = feats_dir / f"{slug}.png"
        if feat_crop.exists():
            view_ctx = next(
                (f.get("viewContext", "") for f in all_features if f.get("featureName") == feature_name),
                "",
            )
            parts.append(f"Feature Crop in context of: {view_ctx}")
            parts.append(encode_image(str(feat_crop)))

    return parts


import re

# ─── Phase 5 prompt ──────────────────────────────────────────────────────────

def phase5_prompt(feature_name: str) -> str:
    return f"""Step 6: Spatial Analysis for Envelope Identification — analyzing ONE specific feature.
You are an expert mechanical engineer and machinist. You must read the manufacturing drawings with deep geometric understanding. You MUST use your Gemini vision capabilities to precisely examine the drawing.
CRITICAL MINDSET SHIFT: Dimensions are a byproduct of geometry, not the other way around. Your NORTH STAR is purely the physical part geometry and its solid material boundaries.

You are provided with view images (labeled "View Name: {{name}}") and data specific to the target feature "{feature_name}".

For the SPECIFIC FEATURE "{feature_name}", perform a rigorous SPATIAL INCORPORATION ANALYSIS:

0. EXISTS AS PHYSICAL FEATURE: Does this feature actually exist geometrically? Or is it an artifact, a pure marking, a duplicate mistake, or a center line? Set 'existsAsPhysicalFeature'.

1. MENTAL TRACE OF OUTER SKIN: Does this specific feature ACTUALLY FORM a portion of the absolute outer boundary?

2. GEOMETRY & LINE NATURE: SOLID MATERIAL ADDITION vs MATERIAL REMOVAL.
   ANY material removal, void, slot, or hole DOES NOT increase the physical bounding box.

3. ENVELOPE CONTRIBUTION ('impactsEnvelope') & AXES:
   - YES: Major structural segment or extreme boundary.
   - NO: Purely internal, material removal, minor local feature, hole/void.
   - MAYBE: Only if highly ambiguous.
   - AXES: Which of Length/Breadth/Height this feature impacts.

4. CORRECTED FEATURE NAME: If the initial name is incorrect, provide correctedFeatureName; else return "{feature_name}" unchanged.

5. MISSED GEOMETRY: List any newly discovered features in 'newFoundFeatures' with featureName, dimensionLxB, reasoning.

Return a JSON array with EXACTLY ONE FeatureVerificationInfo object for: "{feature_name}"."""


_SCHEMA = {{
    "type": "ARRAY",
    "items": {{
        "type": "OBJECT",
        "properties": {{
            "featureName":             {{"type": "STRING"}},
            "correctedFeatureName":    {{"type": "STRING"}},
            "existsAsPhysicalFeature": {{"type": "BOOLEAN"}},
            "visibleInViews": {{
                "type": "ARRAY",
                "items": {{"type": "OBJECT", "properties": {{"viewName": {{"type": "STRING"}}, "isVisible": {{"type": "BOOLEAN"}}}}, "required": ["viewName", "isVisible"]}},
            }},
            "impactsEnvelope": {{"type": "STRING", "enum": ["YES", "NO", "MAYBE"]}},
            "associatedAxes":  {{"type": "ARRAY", "items": {{"type": "STRING"}}}},
            "reasoning":       {{"type": "STRING"}},
            "newFoundFeatures": {{
                "type": "ARRAY",
                "items": {{"type": "OBJECT", "properties": {{"featureName": {{"type": "STRING"}}, "dimensionLxB": {{"type": "STRING"}}, "reasoning": {{"type": "STRING"}}}}, "required": ["featureName", "dimensionLxB", "reasoning"]}},
            }},
        }},
        "required": ["featureName", "existsAsPhysicalFeature", "impactsEnvelope", "associatedAxes", "reasoning"],
    }},
}}


def main():
    args         = json.load(sys.stdin)
    feature_name = args["feature_name"]
    all_features = json.loads(args["all_features_json"])
    all_dims     = json.loads(args["all_dims_json"])
    output_dir   = args["output_dir"]

    contents = _build_payload(feature_name, all_features, all_dims, output_dir)
    contents.append(phase5_prompt(feature_name))

    raw_results = call_gemini(contents, _SCHEMA)
    items = raw_results if isinstance(raw_results, list) else [raw_results]
    vdata = items[0] if items else {}

    # Extract new features from newFoundFeatures
    new_features = [nf["featureName"] for nf in (vdata.get("newFoundFeatures") or []) if nf.get("featureName")]

    result = {
        "featureName":             vdata.get("featureName", feature_name),
        "correctedFeatureName":    (vdata.get("correctedFeatureName") or feature_name).strip() or feature_name,
        "existsAsPhysicalFeature": vdata.get("existsAsPhysicalFeature", True),
        "visibleInViews":          vdata.get("visibleInViews", []),
        "impactsEnvelope":         vdata.get("impactsEnvelope", "MAYBE"),
        "associatedAxes":          vdata.get("associatedAxes", ["None"]),
        "reasoning":               vdata.get("reasoning", ""),
        "new_features":            new_features,
    }
    json.dump(result, sys.stdout)


if __name__ == "__main__":
    main()
