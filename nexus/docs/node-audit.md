# Node-by-node audit

[中文](node-audit.zh.md) | English

Every node type is checked along six dimensions. The point is not to write six documents per type, but to make the six
places that describe a node - executor, static rules, create tools, canvas, docs, tests - say the same thing.

✅ means "enforced by a test or a gate"; the enforcement is listed at the bottom, so a ✅ cannot rot silently.
⬜ means "not audited yet" - it does **not** mean "no test exists".
— means "not applicable": the type has nothing to configure, or the tools cannot create it.

## The six dimensions

| # | Dimension | Question | Where |
|---|---|---|---|
| 1 | Runtime | Does it run, fail and produce the variables the docs promise? `on_error` / post / stream / timeout included | `nexus/service/flow/executors/<type>.go` |
| 2 | Static rules | Do `Step` / `Diagram` findings match dimension 1 - every guaranteed failure reported, no legal config flagged | `nexus/shared/flowcheck/` |
| 3 | Creation | Can `create_node` build a complete one, and do its warnings equal the findings of dimension 2 | `nexus/service/agent/tools/flow_nodes.go` |
| 4 | Canvas | Do the props panel fields, the executor's keys and the doc's key names agree; does the variable picker offer the node's real outputs | `desktop/src/tenant/components/flows/flow/<Type>NodeProps.vue` |
| 5 | Docs & wording | Node page, `SKILL.md`, the README table, the palette label, the wording of every finding code it emits | `skills/flow-authoring/**`, `desktop/src/locales/*/flow.json` |
| 6 | End to end | A minimal `run_flow` per type, or a documented reason why it cannot run without external data | `nexus/service/flow/executors/*_test.go` |

## Status

| Type | Batch | Docs / label | Static rules | Creation | Runtime | Canvas | E2E |
|---|---|---|---|---|---|---|---|
| `code` | 1 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `http` | 1 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `llm` | 1 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `branch` | 2 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `loop` | 2 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `call` | 2 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `result` | 2 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `progress` | 2 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `mcp` | 3 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `rpc` | 3 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `intent` | 3 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `assign` | 3 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `memory` | 3 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `process` | 4 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `function` | 4 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `fork` | 4 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `wait` | 4 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `break` | 4 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `continue` | 4 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `return` | 4 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `exit` | 4 | ✅ | ✅ | ✅ | ⬜ | ⬜ | ⬜ |
| `layout` | 5 | ✅ | ✅ | ✅ | — | ⬜ | — |
| `comment` | 5 | ✅ | ✅ | ✅ | — | ⬜ | — |
| `start` | 5 | ✅ | ✅ | — | — | ⬜ | — |
| `entry` | 5 | ✅ | ✅ | — | — | ⬜ | — |
| `knowledge` | 5 | ✅ | ✅ | — | — | ⬜ | — |

## What is enforced today

- `flowcheck/conformance_test.go` - per type: a complete config produces no finding, an empty one exactly the documented
  codes; the table must cover `knownTypes` in both directions.
- `tools/flow_node_matrix_test.go` - per creatable type: bare creation warns exactly what `Step` says on the default config
  the tool fills in, plus the `return`-inside-a-function path.
- `tools/flow_nodes_test.go` - `TestCreatableNodeTypesFixed` pins the palette list.
- `executors/capabilities_drift_test.go` - error codes, actions, post and fixed outputs equal `flowcheck`'s copy.
- `scripts/check-locale.ps1` - section 8: finding codes ↔ wording; section 9: node type ↔ doc page ↔ palette label.

## Found while auditing

- `create_node` refused a `return` created directly under a function: the guard only looked at ancestors. Fixed in
  `9aeb4e2` (`isOrInside`).
- `sandboxd/config.yaml` kept `langs.js` active while its own comment said js was disabled, so starting sandboxd on a host
  without Node failed. Fixed in `e911cbb`.

## Changing a type

Adding or changing a node type touches, in this order: the executor and its `Capabilities()` (including `FixedOutputs`),
the `Step` / `Diagram` rule plus its row in `conformance_test.go`, the row in `flow_node_matrix_test.go`, the doc page and
the palette label (both gated), then the props panel. A type that skips a step is a type that renders fine and fails at
run time - which is exactly what this audit exists to prevent.
