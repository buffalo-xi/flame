# The Flows screen and the editor

## The list

Filter by status (all / active / deprecated) at the top, search by name and description, with Import and
New flow on the right. Each card shows the name, status, version (`v3` or "unreleased"), protocol (mcp /
rpc or "no endpoint") and who last updated it.

Hovering a card reveals five actions: restore, deprecate, endpoints, export as an asset package, delete.
**Deprecating a flow also retracts every endpoint it exposes**, so callers in production fail immediately.

## The editor has three regions

A vertical toolbar on the left (add node, add comment, variable panel entry), the canvas in the middle,
floating panels on the right (properties, variables, run). Panels can be widened or closed.

**Two ways to add a node**: toolbar "+" → pick a type → click on the canvas to place it; or click the "+"
handle on an existing node → pick a type, and the new node is created to its right, already wired. Comments
do not count against the node limit.

## Hard rules for connections

Source and target handles of the same type cannot connect; no self-links, no cycles, no links across
parent containers. And **a source handle keeps one outgoing edge, a target keeps one incoming edge** —
reconnecting replaces the old edge instead of adding a second one.

## The three variable sources

| Source | Maintained by | Notes |
|---|---|---|
| Flow variables | This flow | Readable and writable in the editor |
| Environment variables | This flow | Travel with the flow |
| Global variables | The tenant, **read-only here** | Maintained in Settings → Global variables; put addresses, accounts and keys here |

Before referencing one in a node's inputs, pick the right source — only then is the template
(`{{.input.x}}` / `{{.local.x}}` / `{{.output.x}}`) resolved.

## Trying it out

"Run" **saves first**, then runs. Results show up in three places: the run panel (this run / history),
the "execution result" sub-page of the variables panel, and the debug highlight on each node (running /
done / skipped / error / breakpoint).

## Save ≠ release ≠ live

- **Save**: writes the draft only.
- **Release current content**: freezes the saved content as a new version; production calls run it
  immediately. Unsaved changes block it.
- **Switch production**: only moves the "which version is live" pointer; the canvas draft is untouched.
- **Load into canvas**: writes that version back into the draft; production keeps the old version until
  you release again. This is also how you throw the draft away and return to what is live.

The version button has three states: "unreleased" (never released, not callable from outside),
"v3 · changed" (production runs v3, the draft is newer), and "v3".

## Endpoints

The Endpoints button in the editor header manages this flow's entries in place, no trip to the endpoints
screen. It refuses to open before the flow has been saved. Endpoint parameters cannot be typed by hand —
they always mirror the start node's inputs; an RPC endpoint is managed by the flow and cannot be deleted
separately.

**Saving with exposed endpoints asks for confirmation**: renaming also renames the exposed tool
(`local__<name>`), and external callers have to be updated.
