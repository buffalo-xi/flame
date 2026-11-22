# The My tenants screen

This is the **tenant owner's** view: it lists only the tenants you own; everyone else's are invisible. The
list is filtered locally with no pagination, and every card starts collapsed — click a title to expand it.

## What you can do yourself

- **Suspend / enable this tenant**: once suspended, the tenant cannot sign in to the console and MCP / RPC
  calls are rejected, effective immediately.
- **The five quotas**: agents, skills, flows, per-session space, sessions per user — bounded by the system
  cap.
- **Flow and agent runtime parameters**: empty falls back to the system level, `-1` means unlimited. The agent
  group has no `-1` — those are safety valves, and "unlimited" there would tear the guard rails down.
- **Reset the root account's password** (`primary`).

## What needs a platform admin

- Transferring the tenant to another owner
- Changing the system cap itself
- Plugin disable policy, capability grants
- Creating or deleting tenants

The footer line "for quota caps and ownership transfer, ask a platform admin" means exactly this: you can
change the quota, not the ceiling.

## Resetting the root account password

It resets only this tenant's root account `primary` and leaves its role and status alone. At least 8
characters, entered twice.

**Sessions already signed in stay valid** — the old password stops working, but whoever is inside stays
inside. To kick them out, suspend the tenant.

## A quota above the cap is rejected outright

Unlike the admin side: an admin entering a value above the cap has it stored but clamped, with a warning;
a tenant doing the same is **rejected** with "cannot exceed the system level".

Read that row's system-cap column first; leaving the field empty falls back to it.

## The remark is written by an admin

The remark in the tenant information block is written by a platform admin and you see the same text. To
change the remark or the display name, ask an admin.
