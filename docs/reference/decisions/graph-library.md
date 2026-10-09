# Decision: DAG layout library

**Chosen:** `@dagrejs/dagre` 2.0.0 (MIT), vendored at
`go/cmd/gateway/web/vendor/dagre.min.js` with its SRI hash, plus Kionga's own SVG
renderer (`pipeline-graph.js`).

## Requirements

Readable automatic layout for small and large DAGs; zoom, pan, fit and a legend;
status shown without relying on colour; keyboard node selection; edges that exactly
match persisted dependencies; offline operation; no bundler (the console is
vanilla JavaScript embedded in the gateway binary).

## Options compared

| Library | Fit | Why not / why |
| --- | --- | --- |
| **Dagre** | Layered layout for DAGs, ~42 KB, no dependencies, works as a classic script | Chosen. Layout only, so rendering, accessibility and interaction stay under our control. |
| ELK.js | Excellent layered layouts for large graphs | ~1.5 MB and asynchronous worker setup; heavier than our graphs (≤200 nodes) need. |
| Cytoscape.js | Full graph toolkit with its own renderer | Canvas renderer makes accessible, keyboard-navigable nodes harder; larger. |
| React Flow / xyflow | Polished node editor | Requires React and a bundler, which the console deliberately avoids. |
| Mermaid | Text-to-diagram | Static output, no node selection or live status. |

## Consequences

- Edges are generated only from each node's `depends_on`; a property test renders
  random DAGs with and without Dagre and asserts the drawn edges equal the
  dependency set.
- Without Dagre (or for a cyclic draft) a dependency-layer fallback layout is used,
  so the graph and its warnings still render.
- Interaction (zoom, pan, fit, roving-tabindex keyboard navigation) is implemented
  in `KiongaPipelineGraph.enhance()` and tested in jsdom.
