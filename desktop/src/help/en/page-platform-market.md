# The Remote market screen

On the left are **the peers I configured** (sites I pull from); on the right is the selected site's catalog,
imported asset by asset.

"Who may pull from us" is not on this screen — that is Local market → Inbound access.

## The five peer fields

| Field | Watch out |
|---|---|
| Site name | Unique; becomes the asset's origin label |
| Address | Host and port only; the path is added for you |
| Auth mode | Market token (the peer requires one) / public (no token) |
| Market token | `flmt_…`; when editing, **empty means keep the current one** |
| Import policy | Approve automatically after import (skips local review) |

Plus an "enable this peer" switch. A path in the address field is the most common configuration mistake.

## Pulling the catalog

Selecting a peer pulls once automatically. The catalog contains **only assets the peer has already approved**;
a failure does not interrupt you with a dialog — the error is stored on the peer and shown on its card.

## Import and overwrite-import

Entries already imported show `imported v{x} → v{y}`, and clicking again is an **overwrite import** (with a
confirmation): the local version goes up by one and, unless "approve automatically" is on, the asset **goes
back to pending review**.

The result tells you which path was taken: "imported and approved (this peer auto-approves)" or "imported,
awaiting local review".

## When the pull fails

Work through the hints on the card: `· no token configured` (token auth chosen but empty), `· disabled` (a
disabled peer is never pulled), the wrong auth mode (a public peer configured with a token, or the reverse),
or a path in the address.

## Who may pull from us

Configured under Local market → Inbound access: anonymous access, an egress token, generating a token. Not
this screen.

## The two "no review" settings are different

- **No review on listing**: whether a tenant's listing into our local market needs review (Local market →
  Listing policy).
- **Peer auto-approve**: whether importing from one peer needs review (Remote market → that peer's import
  policy).

Neither affects the other.
