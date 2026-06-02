#!/usr/bin/env python3
"""
drawing.reconciliation_graph — Tool exec script (phase 9).

Builds the reconciliation graph from phase8 spatial understanding data.
Pure computation — no LLM call. Writes phase9.json to output_dir and returns
the graph as {nodes, edges}.

stdin:  {"spatial_data": "<json>", "output_dir": "..."}
stdout: {"nodes": [...], "edges": [...]}
"""
import sys
import json
from pathlib import Path


def main():
    args         = json.load(sys.stdin)
    spatial_data = json.loads(args["spatial_data"]) if isinstance(args["spatial_data"], str) else args["spatial_data"]
    output_dir   = args["output_dir"]

    # Flatten: map_phase8 returns {"items": [[{...}], [{...}]]} — unwrap nesting
    flat: list[dict] = []
    for item in spatial_data:
        if isinstance(item, list):
            flat.extend(item)
        elif isinstance(item, dict):
            sub = item.get("spatial_data") or []
            if isinstance(sub, list):
                flat.extend(sub)
            else:
                flat.append(item)

    nodes = []
    for idx, su in enumerate(flat):
        nodes.append({
            "id":                        f"rg-{idx}",
            "featureName":               su.get("featureName", ""),
            "dimensions":                su.get("dimensions", []),
            "manufacturing_nature":      su.get("manufacturing_nature", []),
            "skin_or_internal":          su.get("skin_or_internal"),
            "physicalGeometryExplanation": su.get("physicalGeometryExplanation", ""),
            "viewCorrelation":           su.get("viewCorrelation", []),
        })

    edge_set: set[str] = set()
    edges = []
    for src_idx, su in enumerate(flat):
        for fc in su.get("featureCorrelation", []):
            tgt_name = fc.get("relatedFeatureName", "")
            tgt_idx  = next(
                (i for i, s in enumerate(flat) if s.get("featureName") == tgt_name),
                None,
            )
            if tgt_idx is None:
                continue
            fwd, rev = f"{src_idx}-{tgt_idx}", f"{tgt_idx}-{src_idx}"
            if fwd not in edge_set and rev not in edge_set:
                edge_set.add(fwd)
                edges.append({
                    "source": f"rg-{src_idx}",
                    "target": f"rg-{tgt_idx}",
                    "label":  fc.get("relationshipType", ""),
                })

    graph = {"nodes": nodes, "edges": edges}
    Path(output_dir).mkdir(parents=True, exist_ok=True)
    Path(output_dir, "phase9.json").write_text(json.dumps(graph, indent=2))
    json.dump(graph, sys.stdout)


if __name__ == "__main__":
    main()
