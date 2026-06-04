#!/usr/bin/env python3
"""
drawing.build_feature_split — Tool exec script.

Merges phase3 crop data, phase4 dimension data, and phase5 verification
results into the allFeatures structure, then splits into contributing and
non-contributing subsets for phases 6, 7, and unify.

stdin:  {"verifications": "<json>", "all_features_json": "<json>", "all_dims_json": "<json>"}
stdout: {"contributing": [...], "non_contributing": [...], "all_features_built": [...]}
"""
import sys
import json


def _fmt_dim(d: dict) -> str:
    return (
        f"{d.get('dimensionLxB','')} - "
        f"Callout: {d.get('rawTextCallout','')} - "
        f"Details: {d.get('dimensionDetails','')}"
    )


def build_all_features(crops, dims, verifications):
    by_verification = {v.get("featureName"): v for v in verifications}
    out = []
    for c in crops:
        if c.get("isMinor"):
            continue
        fname     = c.get("featureName")
        feat_dims = [d for d in dims if d.get("featureName") == fname]
        out.append({
            "featureName":  fname,
            "dimensions":   feat_dims,
            "vData":        by_verification.get(fname),
            "cropDetails":  c,
        })
    for d in dims:
        if not d.get("isNewFeature"):
            continue
        fname = d.get("featureName")
        out.append({
            "featureName": fname,
            "dimensions":  [d],
            "vData":       by_verification.get(fname),
        })
    return out


def build_contrib_split(all_features):
    contributing     = []
    non_contributing = []
    for f in all_features:
        v      = f.get("vData") or {}
        dim_j  = " | ".join(_fmt_dim(d) for d in f.get("dimensions", []))
        exists = v.get("existsAsPhysicalFeature")
        impact = v.get("impactsEnvelope")
        if v and exists is not False and impact in ("YES", "MAYBE"):
            contributing.append({
                "featureName":    f["featureName"],
                "dimensions":     dim_j,
                "reasoning":      v.get("reasoning"),
                "associatedAxes": v.get("associatedAxes"),
            })
        else:
            reasoning = v.get("reasoning") if v else None
            if not reasoning:
                reasoning = "Not a physical feature" if exists is False else "Unknown"
            non_contributing.append({
                "featureName":    f["featureName"],
                "dimensions":     dim_j,
                "reasoning":      reasoning,
                "associatedAxes": v.get("associatedAxes") if v else None,
            })
    return contributing, non_contributing


def main():
    args         = json.load(sys.stdin)
    verifications = json.loads(args["verifications"]) if isinstance(args["verifications"], str) else args["verifications"]
    all_features  = json.loads(args["all_features_json"])
    all_dims      = json.loads(args["all_dims_json"])

    all_features_built          = build_all_features(all_features, all_dims, verifications)
    contributing, non_contributing = build_contrib_split(all_features_built)

    json.dump({
        "contributing":      contributing,
        "non_contributing":  non_contributing,
        "all_features_built": all_features_built,
    }, sys.stdout)


if __name__ == "__main__":
    main()
