# How to use the "Plugins and capabilities" screen

The left column is the catalog, the right column holds the settings of the selected item. Plugins and
capabilities share this screen, but their defaults point in opposite directions.

## The two groups on the left

- **Plugins (N)**: plugins found on disk, each with one or more implementations.
- **Capabilities (N)**: the tool shapes the platform can grant (exec / long-lived shell / code snippets /
  browser / egress).

The small line under each item is a count: "Disabled for 2/5 tenant(s)" means two tenants were disabled
individually; "Granted to 3/5 tenant(s)" means three tenants have the capability open.

## Two kinds of switches, two different scopes

**The global gate** (top): passes by default. Pulling it is a kill switch — every tenant stops
immediately, and it **overrides** whatever the table says. It does not rewrite those rows; lifting it
restores them.

**Each row in the table**: affects that one tenant only.

## Save the row you changed

The switches are drafts. In the plugin table, each row has to be saved with its own Save button; the
global gate has its own save button at the top. **Switching to another plugin, or hitting Refresh in
the corner, discards anything unsaved.**

## What the labels mean

| Label | Where | Meaning |
|---|---|---|
| Disabled | Plugin row | The platform wrote a disable for this tenant (`allowed=false`) |
| Explicitly allowed | Plugin row | This tenant has a stored "allow". Same effect as the default; legacy leftover, removable via "Clear settings" |
| Allowed by default | Plugin row | No row for this tenant; the default (allowed) applies |
| Set individually / Not opened | Capability row | Has a row of its own / never opened (denied by default) |

## When to use the kill switch

Only when a plugin or capability turns out to be unsafe and **every tenant must lose it right now**.
To stop one tenant, change that tenant's row and leave the gate alone — the gate affects everyone.

While the gate is blocking, the rows **can still be edited**: the stored setting and the kill switch
are separate things, so you can prepare the configuration and have it take effect the moment the gate
is lifted.
