#!/usr/bin/env python3
"""
image.crop_dims — Tool exec script.

For each view, crops every dimension callout bounding box (from phase4) out of
that view's cropped image. Produces all_dims_json and per-feature dim crop data.

stdin:  {"views": "<json>", "phase4_results": "<json>", "output_dir": "..."}
stdout: {"all_dims_json": "<flat JSON>", "dim_data_by_feature": {...}}
"""
import sys
import json
import re
from collections import defaultdict
from pathlib import Path
from PIL import Image


def safe_name(s: str) -> str:
    return re.sub(r"[^a-zA-Z0-9_-]", "_", s).strip("_")[:80]


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
    views          = json.loads(args["views"])
    phase4_results = json.loads(args["phase4_results"])  # [[{featureName,...}],...]
    output_dir     = args["output_dir"]

    dims_dir = Path(output_dir) / "crops" / "dims"
    dims_dir.mkdir(parents=True, exist_ok=True)

    all_dims: list[dict] = []
    dim_data_by_feature: dict[str, dict] = defaultdict(lambda: {"dims": [], "dim_crop_paths": []})

    for view_info, dims_for_view in zip(views, phase4_results):
        view_crop_path = view_info["path"]
        view_img = Image.open(view_crop_path).convert("RGB")
        dims = dims_for_view.get("dimensions") if isinstance(dims_for_view, dict) else dims_for_view

        for i, dim in enumerate(dims or []):
            fname  = dim.get("featureName", "unknown")
            slug   = safe_name(f"{fname}_{i}")
            crop   = crop_bbox(view_img, dim["ymin"], dim["xmin"], dim["ymax"], dim["xmax"])
            path   = str(dims_dir / f"{slug}.png")
            crop.save(path, "PNG")
            all_dims.append(dim)
            dim_data_by_feature[fname]["dims"].append(dim)
            dim_data_by_feature[fname]["dim_crop_paths"].append(path)

    json.dump({
        "all_dims_json":      json.dumps(all_dims),
        "dim_data_by_feature": dict(dim_data_by_feature),
    }, sys.stdout)


if __name__ == "__main__":
    main()
