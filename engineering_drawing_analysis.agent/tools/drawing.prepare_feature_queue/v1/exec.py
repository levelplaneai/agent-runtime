#!/usr/bin/env python3
"""
drawing.prepare_feature_queue — Tool exec script.

Extracts a deduplicated, ordered list of unique feature names from the phase4
dimension data. The loop_phase5 node seeds its queue from this list.

stdin:  {"all_dims_json": "<json string of dimension array>"}
stdout: {"feature_names": ["Feature A", "Feature B", ...]}
"""
import sys
import json


def main():
    args         = json.load(sys.stdin)
    all_dims     = json.loads(args["all_dims_json"])
    seen         = dict.fromkeys(d["featureName"] for d in all_dims if d.get("featureName"))
    feature_names = list(seen.keys())
    json.dump({"feature_names": feature_names}, sys.stdout)


if __name__ == "__main__":
    main()
