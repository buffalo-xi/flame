# The Flows screen and the editor

## The list

Filter by status (all / active / deprecated) at the top, search by name and description, with Import and
New flow on the right. Each card shows the name, status, version (`v3` or "unreleased"), protocol (mcp /
rpc or "no endpoint") and who last updated it.

Hovering a card reveals five actions: restore, deprecate, endpoints, export as an asset package, delete.
**Deprecating a flow also retracts every endpoint it exposes**, so callers in production fail immediately.
**Deleting a flow deletes its run history too** (it disappears from the run history screen), together with
endpoints that are not exposed; retract any exposed endpoint first.

With no workflow at all, the list area explains what a workflow is and offers a **Create a workflow** entry, so
the top bar is not the only way in: ask the main agent in chat to build one ("build me a workflow that totals
yesterday's orders by region and writes a short summary"), or draw it by hand from that button. A status filter
or a search that matches nothing shows a single line instead.

## The editor has three regions

A vertical toolbar on the left (add node, add comment, variable panel entry), the canvas in the middle,
floating panels on the right (properties, variables, run). Panels can be widened or closed.

**Two ways to add a node**: toolbar "+" → pick a type → click on the canvas to place it; or click the "+"
handle on an existing node → pick a type, and the new node is created to its right, already wired. Comments
do not count against the node limit.

The "+" panel has two tabs. **Nodes** builds an empty node of a type; **Tools** lists what is already wired up —
the RPC endpoints this tenant exposes and the tools under its MCP / RPC connections. Picking one drops a node
with the connection and tool already chosen, plus a sample of the arguments built from the interface's declared
input schema (required fields only, values are type placeholders — replace them and it runs). The new node is
selected and its properties panel opens on its own, with the sample already in the arguments box. Switching the tool in
that panel replaces the arguments with the new tool's schema, and so does switching the endpoint on an RPC node.
Anything already in the box is overwritten — copy what you want to keep first; an interface that declares no arguments
leaves an empty `{}`. The "?" next to the tool dropdown opens the selected tool's details: its description, every
argument with type, required flag and meaning, and the raw schema. It is greyed out until a tool is chosen. In endpoint
mode an RPC node's endpoint dropdown carries the same "?", showing the endpoint's target type, exposure state and the
same argument table. Connections stay
collapsed in that list and load their tools on expand; an endpoint that points back at the flow you are editing
is never listed (calling itself cannot work). When the list is empty, add a connection under Settings → Tools, or
expose a flow as an endpoint.

## ESC and leaving the editor

ESC walks out one layer at a time: the context menu and dropdowns first, then the right-hand panels (properties, variables, run) and the version panel; only when none of them is open does it leave the editor for the list. While renaming in the header, ESC cancels just that rename and stays on the page.

**Leaving with unsaved edits asks first** (the back button does the same) — unsaved edits are gone once you leave; the editor does not save them for you.

## Hard rules for connections

Source and target handles of the same type cannot connect; no self-links, no cycles, no links across
parent containers. And **a source handle keeps one outgoing edge, a target keeps one incoming edge** —
reconnecting replaces the old edge instead of adding a second one. Terminal nodes (`exit` / `break` / `continue` /
`return` / raise error) take an incoming edge only: a raise always ends in failure, so a node wired after it would never run.

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

When a node fails, "Error" is its own code and message. If the handler or the post-processing script it
configured under error handling failed **on its own**, the execution result adds "Handler error" / "Post-processing
error" lines — the node still fails with its original error, and its code is not rewritten.

## Concurrent members

A `process` / `loop` body is one concurrency scope. Tick **Run concurrently in the parent body** on one of its **direct
children** and that node becomes a member: the body dispatches it, keeps walking the chain, and joins every member when the
body ends. Only steps that can wait carry that tick: the entry, `break` / `continue` / `return` / `exit`, the comment /
layout nodes, `result` / `progress`, `assign` / `memory` and the raise node do not — control flow stays on the chain (an `exit` written
inside a member's own body still ends the run), result / progress append to this run in call order, so such a member would
take whatever slot the scheduler gave it, and assign / memory never wait, so a slot buys nothing.

Membership lives in the node's own data (`data.concurrent` — the tick and the tool argument both write it). On the canvas a
member's card shows a small blue concurrency icon next to its title, with the tick's label on hover; the icon only surfaces
the flag, changing it still happens in the properties panel.

- The host sets the limit and the join mode under **Concurrency scope**: a new flow segment starts at 8; with the limit
  switch off there is no author limit, but the runtime still queues behind the tenant ceiling (Settings → workflow run
  limits → max concurrency, fallback 16), so members beyond it wait for a slot instead of failing. `collect` (the default)
  lets every member run to its own end; `drain` stops starting new members after the first failure (queued members and
  later chain nodes are recorded `skipped`).
- `abort` adds to `drain`: the members still running are cancelled (recorded `cancelled`, reason `aborted by scope`), so a
  host that has already failed stops paying for work nobody will read. The body's own chain is not interrupted — it stops
  at the next node boundary.
- **Concurrency scope** also takes a scope timeout in seconds: when it expires the members still running are cancelled, the
  host is recorded `failed` with `err_code: timeout`, the message lists every member's state at that moment, and the
  snapshot is marked `abandoned`. It is a different level from a node timeout — a node timeout ends that one node, a scope
  timeout ends this body's concurrency.
- A member failure fails the run on its own, without anything reading its result. To make a failed section survivable, use the
  **host's own** error handling: **skip** continues the main chain past it (the host is recorded `skipped`, every member keeps
  its own record, so the failure stays visible), **handler** may rewrite the error code / message or return skip, and
  **post** writes the fallback values (`set_outputs`, e.g. `ok = false`) — a handler cannot write outputs. A host has
  no retry — re-running the whole body would repeat every member's side effects, which is not "retry the failed member". A
  member's own error handling runs first and is usually enough.
- Members cannot read each other's results, and a chain node cannot read a member: to feed A's value into B, use two hosts
  and read the first host's `outputs` in the second.
- The top level, a `function` body and a `branch` lane are not scopes: ticking the box there is refused on save.

## The pre-save check

Pressing Save (and the automatic save before a run) first hands the canvas to the backend for a static check. Nothing is
executed: config-block shape, container entries, cross-container edges, dangling `source_ref` / `call.target`,
branch conditions with no outgoing edge, a `loop each` source the inputs do not declare, unreachable
nodes, `on_error` actions / error codes the node does not support or can never produce, a code listed under both skip and
handler (skip matches first, so that handler never runs), and a step that skips without a
fallback while a downstream node reads its outputs — a skipped node leaves an empty value that is indistinguishable from a
real empty result, so that pair is reported as a warning.

The result is listed as errors and warnings, each line naming the node and the config key:

- **error**: as written, it will not run (a code step with no script, a branch with no children, a script language the
  sandbox does not offer, a break outside any loop);
- **warning**: it runs, but probably not as intended (undeclared `outputs`, a fallback rule that can never fire, an
  `assign` with nothing to assign, a branch child with no condition).

The check also re-runs while you edit (about a second after you stop), so saving is not the first time you hear about a
problem: a node with findings gets a small round badge in its title row — red for an error, amber for warnings only. Hover
it for the text and the config key, click it to jump to that node. Each line in the pre-save dialog is the same shortcut:
click it to drop this save and jump to that node with its settings open.

The check only informs and never blocks the save — the canvas is a draft, and a half-built graph is a normal state. Only
the hard server-side rules block a write (duplicate labels, the node quota). Once you have answered "save anyway" for one
list on a flow, the same list does not pop up again; it returns only when it changes (one item fixed, another one
appeared). The agent gets the same list from `validate_flow` in a conversation, so "the canvas said it was fine, then the
run failed" cannot come from two rule sets.

## Save ≠ release ≠ live

- **Save**: writes the draft only.
- **Release current content**: freezes the saved content as a new version; production calls run it
  immediately. Unsaved changes block it.
- **Switch production**: only moves the "which version is live" pointer; the canvas draft is untouched.
- **Load into canvas**: writes that version back into the draft; production keeps the old version until
  you release again. This is also how you throw the draft away and return to what is live.
- **Unrelease**: takes the current version off production by clearing the "which version is live"
  pointer only; the history, the canvas and the endpoints stay, official calls report "no released
  version", and you can release again later. **A flow with a released version cannot be deleted** —
  unrelease first, then delete (a delete should not double as a service stop).

The version button has three states: "unreleased" (never released, not callable from outside),
"v3 · changed" (production runs v3, the draft is newer), and "v3".

## Endpoints

The Endpoints button in the editor header manages this flow's entries in place, no trip to the endpoints
screen. It refuses to open before the flow has been saved. Endpoint parameters cannot be typed by hand —
they always mirror the start node's inputs; an RPC endpoint is managed by the flow and cannot be deleted
separately. A flow that has never released a version cannot be exposed — release it first.

**Saving with exposed endpoints asks for confirmation**: renaming also renames the exposed tool
(`local__<name>`), and external callers have to be updated.
