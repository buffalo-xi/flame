# The Local market screen

The platform **governs here, it does not carry**: asset snapshots are produced by tenants listing or by
import, and installation happens on the tenant side. This screen only changes status and visibility.

## The three sub-tabs

- **All assets**: cards grouped by status; the badge shows the **pending count**, not the total.
- **Listing policy**: whether a tenant's listing needs review.
- **Inbound access**: which external sites may pull from us (see also the Remote market screen).

## All assets, grouped by status

Pending / listed / rejected / delisted / draft / other. The order is the order you work in — pending at the
top, drafts at the bottom; empty groups are hidden and every group header collapses.

Status is one-way: `draft → pending → listed or rejected`, `listed → delisted`. Skipping a step is rejected
by the backend.

## The actions appear per status

Export package (when there are files), approve / reject (pending only), delist (listed only), delete (listed
or delisted). Pending, rejected and draft entries **cannot be delisted or deleted** — the buttons are not
even rendered.

## Approve and reject

- **Approve**: the asset becomes installable for the tenants inside its visibility right away; a review note
  is optional.
- **Reject**: the status becomes "rejected" and **the reason is required**. The provider sees that text under
  "My assets" in the tenant console and has to list again (version +1) to re-enter the queue.

## Listing policy: a global default plus per-tenant overrides

One switch (review required / no review, **default is no review**), with per-tenant overrides of
"follow default / must review / no review". It **only affects later listings, not what is already listed**.

## Delist ≠ delete

- **Delist**: withdrawn but kept. It disappears from every tenant's market immediately, the record stays, and
  it can still be deleted later.
- **Delete**: wiped. Snapshot, visibility and install records all go, **not recoverable**.

Neither touches copies already installed in a tenant. Delete does not require delisting first.

## About the wording

The screen says "All assets", "Pending" and "Listing policy". The "asset pool" and "intake review" that show
up in code comments are not user-facing copy — use the screen's words when talking to people.
