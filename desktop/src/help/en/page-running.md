# The Running screen

Three tabs at the top, **three different data sources that cannot query each other**: running, flow
history, agent history.

## Running

The agents and flows executing in this tenant right now. The data comes from the backend's cross-instance
record (Redis), not from this page's memory, and only this tab polls.

The toolbar filters by type, by longest/newest first, and by a minimum duration. Rows over 30 minutes get
an orange bar on the left and orange duration text. Each row can only be **terminated**; the confirmation
has three outcomes: terminated / broadcast / not found (meaning it had already finished).

When the ledger cannot be read (Redis unavailable) a yellow bar warns you and the list only shows what this
instance is running.

## Flow history

Filter by flow, initiator, session id, status (succeeded/failed/cancelled) and time range. Twenty rows per
page, with "load more" appending.

The detail drawer shows: run id, flow, user, session, status, entry, start/end, duration, error, and a node
table (node, status, duration, error). A run stopped by an `exit` node still counts as succeeded, so the
drawer adds a "stop reason" row naming that node (the node table also shows it) — that is the only way to
tell "ran to the end" from "cut short". **No inputs/outputs, and no re-run** — node variables and logs live
in the flow editor's run panel. Export to CSV and conditional bulk cleanup are available.

## Two 24-hour traps

- The time range is a fixed window computed **when the page was opened**; it does not slide while the page
  sits open. After midnight, re-apply a shortcut to move it.
- "Filter by this session" **clears the time range**. Otherwise that session's earlier runs are silently
  filtered out by the default 24 hours and look lost.

"Cleanup" starts disabled: the default 24 hours does not count as a filter, so you must actually pick one.

## Agent history

Agent-side history, queried separately from flow history. To see which nodes ran and what the variables
held, go to the corresponding editor.

## Do not expect to see live runs here

"Running" comes from the live ledger, history comes from database records. A running call never appears in
history, and a finished run never appears under "Running".
