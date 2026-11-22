# The System settings screen

Domain navigation on the left, four collapsible cards on the right: quota caps, flow runtime parameters,
agent runtime parameters, LLM provider presets. Each card loads, validates and saves on its own.

## Quota caps

Five items: agents, skills, flows, per-session space (MiB), sessions per user. The table has three columns —
item, fallback value, system setting — and only the third is editable.

## Runtime parameters

Seven for flows (max timeout, recursion depth, node count, execution count, loop count, run records,
session memory) and six for agents. Each has a one-line explanation; `-1` means unlimited.

## Empty, -1 and 0 are three different things

- **Empty** = not set, falls back to the "fallback value". That value is the built-in default, read-only.
- **-1** = unlimited.
- **0** = nothing may be created.

Flows and quotas accept `-1`; the agent group holds safety valves and does not. Session space is entered in
MiB and stored in bytes.

## System values are a hard cap, not an override

At runtime the value is **min(tenant value, system value)**. Lowering a system value puts a ceiling on
every tenant; it does not rewrite their values, delete existing objects, or touch tenant rows.

Saving asks for confirmation and lists every change as "old → new", because one save can change several.

## LLM presets are globally shared

These presets are shared by **all tenants**. When a tenant clicks one under Settings → LLM configuration,
the name, protocol and URL are **copied** into their own record, and each keeps its own version afterwards
(including its own API key).

So deleting a preset does not affect tenants that already copied it.
