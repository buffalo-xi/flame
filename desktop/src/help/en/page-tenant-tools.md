# How to read the Tools screen

Three tabs, three independent counts: **the tool catalog excludes plugins and custom tools**, so only the
three numbers added together make up the whole.

## The four sections of the catalog

| Section | What it is | What you can do |
|---|---|---|
| Tool groups | Sets you build yourself | Create / edit / delete; members are single tools or natural groups, never nested |
| Built-in tools | Shipped with the code, read-only | Details, see who uses it |
| Remote connections | MCP / RPC we call out to | Refresh the list, check availability, see references |
| Local flows | This tenant's rpc endpoints | Whether it is exposed or internal, jump to the endpoints screen |

## This screen does not grant anything

Granting happens in **Agents → Settings → Tool configuration**: the "Tool grant" sidebar has three
sections (required / usable / denied), and changes only take effect once you save the configuration.

The tools screen answers "what do we have and can it be used", not "which agent may use it".

## There are five reasons a tool is unavailable

Platform has not opened the capability, platform disabled it, the plugin is not enabled, the connection
is not enabled, the endpoint is not exposed. The tool is still listed, greyed out; "Details" shows which
one applies.

**Platform policy wins**: tenant-side grants, and even the "unrestricted tools" switch, cannot override a
platform capability that was never opened or was disabled.

## The plugins tab

The switch in the corner only submits enabled / disabled; it does not touch configuration. The
implementation is picked in the "Configuration" sidebar:

- **Main implementation**: the model only sees this one.
- **Fallback chain**: retried in order when the main one fails. Configuration errors (a missing key, for
  example) do not fall back.
- "Try once" runs the current configuration for real.

When the platform disabled it, the switch is greyed out and a yellow bar says whether it is a global
disable or one for this tenant.

## The custom tools tab

The implementation is a **script** (python / js / lua / sh), not HTTP. The order is: create → save (the
`tools/<name>/` directory is only created on save) → edit the code in the directory editor → try it →
release a version.

Note: "try" only runs **saved** code, so the button is greyed out while the form or a file has unsaved
changes; "allow network access" additionally needs the egress capability opened by the platform.

## Do not mix up the two directions

- **Remote connections**: we call out and use someone else's tools.
- **Local flows**: we expose our own rpc endpoints to others; the exposure switch lives on the endpoints
  screen, not here.
