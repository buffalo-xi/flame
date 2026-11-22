# Common misreadings

These are the mistakes that come from treating two similar actions as one. Each entry is written as
"what it looks like → what it actually is".

## "Clear settings" points in opposite directions

- **Plugin**: clear = delete this tenant's disable row → back to the default (allowed) → **opens it up**.
- **Capability**: clear = delete this tenant's grant row → back to the default (denied) → **closes it**.

Same action, two outcomes. Read the row first: "Reset to default (allow)" or "Reset to not opened".

## Suspend a tenant ≠ delete a tenant

Suspending only blocks sign-in and calls; all data stays and you can enable it again. Deleting wipes
that tenant's schema and file directory, is **not recoverable**, and an active tenant has to be
suspended before it can be deleted.

## Delist ≠ uninstall

Delisting withdraws an asset from the marketplace: nobody can install it any more, but **copies already
installed keep running**. To stop someone's running copy, disable the tenant or the plugin — delisting
will not do it.

## Retract ≠ delete an endpoint

Retracting only stops calls made with external credentials; the endpoint row, its input schema and its
binding all stay, and exposing it again restores them. Deleting removes the row; an exposed endpoint
has to be retracted first.

## "Load into draft" does not touch production; "switch production" does not touch the draft

- **Switch production**: production immediately runs that version; the draft stays as it was.
- **Load into draft**: that version's content is written back into the draft for further editing;
  production keeps running the current version until you release again.

For a change to reach callers, it has to be released.

## Disable / intercept are not the same word

Plugins use "disable / lift the disable"; capabilities use "intercept / lift the interception". This is
not a matter of style: the two domains have separate tables and opposite defaults, and mixing the words
makes people think changing one changes the other.

## "Not set" is not "off"

- Plugin not set = allowed (the tenant can enable it and use it)
- Capability not set = not opened (denied)

To decide whether something is usable, read the **effective** column, not whether it is "not set".

## Visible ≠ written into the prompt

Marking a skill visible to everyone, or adding it to a whitelist, only makes it **discoverable**. To get it
into a specific agent's prompt, bind it in that agent's "Call grants → Usable skills"; only the name and
description are written.

## A skill declaring a tool ≠ a tool grant

Ticking "I need exec" inside a skill declares a requirement. The agent running the command still has to have
the capability opened by the platform and the tool granted to it, or the call is rejected anyway.

## A quota above the cap behaves differently on each side

A platform admin entering a value above the system cap: it is stored but clamped, with a warning at the
bottom of the card. A tenant doing the same is **rejected** and cannot save.
