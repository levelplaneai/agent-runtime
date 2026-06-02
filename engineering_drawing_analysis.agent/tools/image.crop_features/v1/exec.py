#!/usr/bin/env python3
"""
image.crop_features — Tool exec script.

For each view, crops every atomic feature bounding box (from phase3) out of
that view's cropped image. Produces the phase4_input structure ready for
map_phase4, plus a flat all_features_json string.

stdin:  {"views": "<json>", "phase3_results": "<json>", "output_dir": "..."}
stdout: {
  "phase4_input": [
    {
      "view_name": "Front View",
      "view_crop_path": "/abs/path/front_view.png",
      "features_with_crops": [{"featureName": "Bore A", "crop_path": "..."}]
    }
  ],
  "all_features_json": "<flat JSON string of all features>",
  "non_geometric_views": ["Title Block", ...]
}
"""
import sys
import json
import re
from pathlib import Path
from PIL import Image

_NON_GEOMETRIC = (
    "titleblock", "title", "revision", "note",
    "technicalrequirement", "partslist", "billofmaterial", "legend",
)


def safe_name(s: str) -> str:
    return re.sub(r"[^a-zA-Z0-9_-]", "_", s).strip("_")[:80]


def is_non_geometric(view_name: str) -> bool:
    n = re.sub(r"[^a-z0-9]", "", view_name.lower())
    return any(kw in n for kw in _NON_GEOMETRIC)


def crop_bbox(img: Image.Image, ymin, xmin, ymax, xmax) -> Image.Image:
    w, h = img.size
    left   = int(xmin / 1000 * w)
    right  = int(xmax / 1000 * w)
    top    = int(ymin / 1000 * h)
    bottom = int(ymax / 1000 * h)
    left, right = min(left, right), max(left, right)
    top, bottom = min(top, bottom), max(top, bottom)
    left   = max(0, min(left, w - 1))
    right  = max(left + 1, min(right, w))
    top    = max(0, min(top, h - 1))
    bottom = max(top + 1, min(bottom, h))
    return img.crop((left, top, right, bottom))


def main():
    args = json.load(sys.stdin)
    views          = json.loads(args["views"])          # [{name, path}]
    phase3_results = json.loads(args["phase3_results"]) # [[{featureName,...}],...]
    output_dir     = args["output_dir"]

    feats_dir = Path(output_dir) / "crops" / "features"
    feats_dir.mkdir(parents=True, exist_ok=True)

    phase4_input   = []
    all_features   = []
    non_geometric  = []

    for view_info, features in zip(views, phase3_results):
        view_name      = view_info["name"]
        view_crop_path = view_info["path"]

        if is_non_geometric(view_name):
            non_geometric.append(view_name)
            continue

        view_img = Image.open(view_crop_path).convert("RGB")
        features_with_crops = []
        for feat in (features.get("features") or features if isinstance(features, list) else []):
            fname   = feat.get("featureName", "unknown")
            slug    = safe_name(fname)
            crop    = crop_bbox(view_img, feat["ymin"], feat["xmin"], feat["ymax"], feat["xmax"])
            path    = str(feats_dir / f"{slug}.png")
            crop.save(path, "PNG")
            features_with_crops.append({"featureName": fname, "crop_path": path})
            all_features.append(feat)

        phase4_input.append({
            "view_name":           view_name,
            "view_crop_path":      view_crop_path,
            "features_with_crops": features_with_crops,
            "feature_names":       [f["featureName"] for f in features_with_crops],
        })

    json.dump({
        "phase4_input":       phase4_input,
        "all_features_json":  json.dumps(all_features),
        "non_geometric_views": non_geometric,
    }, sys.stdout)


if __name__ == "__main__":
    main()
