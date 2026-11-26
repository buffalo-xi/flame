# Save, release, switch production, load into draft, unrelease

Agents, skills, custom tools and flows all have versions. Five actions look alike; their scopes are not.

## What each action does

| Action | Touches the draft | Touches production | When to use it |
|---|---|---|---|
| Save | writes | no | Day-to-day editing; save as often as you like |
| Release | reads (must already be saved) | moves to the new version | You want production calls to use the new content |
| Switch production | **no** | moves to an older version | Something went wrong and it must be rolled back now |
| Load into draft ("load into canvas" for flows) | overwrites | **no** | Start over from an old version, or drop the draft back to what is live |
| Unrelease | no | **takes the current version off production** (history, endpoints and permissions untouched) | You want to take it down for a while, or you are about to delete the entity |

## An entity with a released version cannot be deleted

Before an entity (agent / skill / custom tool / flow) can be deleted, nothing may be released: with v3 live the
delete is refused and the answer points at "unrelease". Unrelease only clears the live pointer — the draft and the
history stay, so you can release again later; after a delete the version history goes with the entity. Deprecating
does not block a delete: deprecation is already the stop step.

## The draft and production are two different things

The draft is what you are editing; production is what real calls (MCP / RPC / endpoints / other flows)
actually run.

So "saved" does not mean "live": saving writes the draft, production still runs the previous version
until you release.

## The three states in the header

- **Unreleased**: never released, not callable from outside.
- **v3 · changed**: production runs v3 and the draft is newer — calls still hit v3.
- **v3**: the draft and production match.

The "unreleased" tag on the list card says the same thing: there is no formal version a caller can use.

## Grants and bindings do not travel with a version

A version carries the **definition** only. **Who may call it** (grants, skill bindings) is always the
current configuration, and rolling a version back does not roll them back — they stay what they were.

The exception: an agent's "unrestricted tools" and "skill discovery" switches are part of the
definition, so loading a draft version writes those back.

## Why "load into draft" is not a rollback

A rollback means "production changes now", and that is **switch production**. Loading into the draft only
changes your working copy — production is untouched, so using it as a rollback looks like it worked when
nothing happened.
