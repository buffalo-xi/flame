# The Skills screen

## The list

Filter by status (all / active / deprecated), search by name and description, the "system" toggle, Import
and New skill.

The tags keep three things apart: version (`v3` / "unreleased") and scope (private / shared N). "Shared N"
is how many agents can see it — the agent total when it is visible to everyone, the bound count for a
whitelist, and it turns red when a whitelist has nobody in it. Hovering reveals: restore, deprecate, edit,
export, delete. **A delete needs nothing released**: with v3 live it is refused until you unrelease in the
version panel; a deprecated skill (and a private one, whose version follows its agent) can be deleted directly.

When the list shows no skill of your own — only the system built-ins, or nothing at all with the system toggle off —
a guide appears below: what a skill is, how to have the main agent write one in chat, and how to create one by hand.
It stays out of the way while you are searching.

## The content directory and the editor

Creating a skill generates a `SKILL.md` skeleton on the backend and opens it. The left side is a
breadcrumb directory (new folder/file, upload, multi-select delete, refresh); the right side is a tabbed
editor.

## ESC and leaving

ESC walks out one layer at a time: the settings panel first, then the editor back to the list. While renaming in the header, ESC cancels just that rename. Leaving with unsaved files asks first, and the back button does the same.

There is no fixed subdirectory layout; `references/` for documents and `scripts/` for scripts is the usual
shape. Every file opens as plain text, and only `.md` files get an edit / rendered-preview switch.

## Private skills

A skill that belongs to an agent is private. **The owner is decided at creation time and cannot be
changed**, so changing it means recreating the skill.

Three consequences: it cannot be exported on its own (export the owning agent instead), it cannot be listed
on its own, and it has no version of its own (the version follows the agent, and the header becomes a
one-line hint).

## Three tabs in the settings panel

- **Visibility**: everyone, or a whitelist. Picking in the whitelist saves immediately.
- **Tool dependencies**: declares which tools this skill needs. **This is a declaration, not a grant** —
  tick, then press Save below.
- **Properties**: owner (only at creation), user-callable, command dispatch and target tool. Target-tool
  candidates come from Tool dependencies; a whole-group dependency (for example "Built-in · exec") expands into
  the group's individual tools, and a tool name can also be typed directly.

The save semantics differ per tab: visibility and properties apply immediately, tool dependencies need the
explicit Save.

## Visible ≠ written into the prompt

A whitelist only decides **which agents can discover it**. For it to actually appear in an agent's prompt,
bind it in that agent's Settings → Call grants → Usable skills, where a click binds or unbinds. Only the
name and description are written into the prompt, not the whole body.

## Tool dependencies ≠ tool grants

A skill declaring "I need exec" does not mean the agent calling it has exec. The agent still has to have
the capability opened by the platform and the tool granted to it.

## Versions

Same scheme as agents: unreleased / vN / vN · changed, and the drawer offers "Release current draft",
"Switch production" and "Load into draft". While a skill is deprecated everything is blocked, including all
four version actions.
