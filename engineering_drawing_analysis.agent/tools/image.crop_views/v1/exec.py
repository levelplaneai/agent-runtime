#!/usr/bin/env python3
"""
image.crop_views — Tool exec script.

Reads the drawing (PDF or image), crops each view bounding box detected by
phase2a, and saves one PNG per view. Runs with the tool's version directory as
CWD; no server or ports required.

stdin:  {"drawing_path": "...", "view_boxes": "<json>", "output_dir": "..."}
stdout: {"views": [{"name": "Front View", "path": "/abs/path.png"}, ...]}
"""
import sys
import json
import os
import re
from pathlib import Path

# ── dependency bootstrap ──────────────────────────────────────────────────────
import importlib.util, subprocess as _sp

def _ensure(pkg, import_as=None):
    if importlib.util.find_spec(import_as or pkg) is None:
        _sp.check_call([sys.executable, "-m", "pip", "install", "-q", pkg],
                       stdout=_sp.DEVNULL)

_ensure("Pillow", "PIL")
_ensure("pdf2image")

from PIL import Image
from pdf2image import convert_from_path

# ─────────────────────────────────────────────────────────────────────────────

def safe_name(s: str) -> str:
    return re.sub(r"[^a-zA-Z0-9_-]", "_", s).strip("_")[:80]


def load_drawing(path: str) -> Image.Image:
    p = Path(path)
    if p.suffix.lower() == ".pdf":
        pages = convert_from_path(str(p), dpi=150, first_page=1, last_page=1)
        return pages[0].convert("RGB")
    return Image.open(path).convert("RGB")


def crop_bbox(img: Image.Image, ymin, xmin, ymax, xmax) -> Image.Image:
    """Crop from 0-1000 normalised coordinates."""
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
    drawing_path = args["drawing_path"]
    view_boxes   = json.loads(args["view_boxes"])  # list of {viewName, ymin, xmin, ymax, xmax}
    output_dir   = args["output_dir"]

    views_dir = Path(output_dir) / "crops" / "views"
    views_dir.mkdir(parents=True, exist_ok=True)

    img = load_drawing(drawing_path)
    result = []
    for view in view_boxes:
        name  = view["viewName"]
        crop  = crop_bbox(img, view["ymin"], view["xmin"], view["ymax"], view["xmax"])
        fname = safe_name(name) + ".png"
        path  = str(views_dir / fname)
        crop.save(path, "PNG")
        result.append({"name": name, "path": path})

    json.dump({"views": result}, sys.stdout)


if __name__ == "__main__":
    main()
