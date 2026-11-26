# The Agents screen

## The list

Filter by status (all / active / deprecated, with counts), search by name and description, with Import and
New agent on the right. There is also a "system" toggle that only decides whether the built-in protected
agents (such as main) are shown; it changes no data.

A card shows name, status, version (`v3` / "unreleased"), endpoint protocol tag, description and who last
updated it. Hovering reveals five actions: restore, deprecate, endpoints, export as an asset package,
delete.

## The editor is a directory of files

Clicking a card opens a full-screen editor: a directory tree on the left, a tabbed text editor on the
right. `AGENT.md` at the root is the agent's definition — it opens automatically and **can be edited but
not deleted**. The `skills/` directory is read-only and maintained from the Skills screen.

## Two tabs in the settings panel

"Settings" in the corner opens a right-side drawer:

- **Call grants**: usable skills, whether unbound skills may be discovered, default reply language, super
  privileges, plus the two directions "who may call it" / "what it may call".
- **Tool configuration**: the unrestricted-tools switch, adding tools and groups, and the three grant
  sections.

## Tool grants come in three sections

Required / usable / denied. "Add tool / group" slides in a sidebar from the left: pick the section first,
then click a group card or a single tool.

**Changes take effect only when you press Save in the configuration panel** — they are not applied
immediately.

## The Save button only saves the current file

This is the easiest one to get wrong: the Save button in the header writes the currently open file and
nothing else. Everything in the settings panel has its own path — tool grants need the panel's Save, while
skill bindings and the two switches save on click.

## Versions

The version badge has three states (unreleased / v3 / v3 · changed) and opens the version drawer: "Release
current draft", "Switch production", "Load into draft". Releasing does not touch the draft, switching only
moves the production pointer, loading only rewrites the draft. For the full comparison see "Save, release,
switch production, load into draft" in the help center.

## Endpoints

Agents can be exposed too. The entry is the "endpoints" icon on a hovered card, which opens a right-side
drawer: the protocol is MCP or RPC and only one of each, the endpoint name is the entity name and cannot be
changed, and an agent declares no inputs — callers pass free-form JSON. An exposed endpoint cannot be
deleted; deprecating the agent retracts all of its endpoints automatically. An agent that never released a
version cannot be exposed — release it first. The super agent is exempt: it always runs the draft.

## Three things that are easy to confuse

- **The "system" toggle**: show/hide only, no data change.
- **"Deprecated"**: the whole agent becomes read-only, and one click restores it.
- **The purple shield**: super privileges, not "built in".
