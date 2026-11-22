# The Settings screen

The left side is the section navigation; the search box at the top jumps to a setting and highlights it.

## The eleven sections

LLM configuration, MCP connection (outbound), RPC connection (outbound), MCP authentication (inbound), RPC
authentication (inbound), service credentials, user management, global variables, tenant quota, flow
runtime parameters, agent runtime parameters.

"Outbound" is us calling someone else's service; "inbound" is how others authenticate when calling us —
two directions, not to be mixed up.

## Global variables

**"Global" here means global within this tenant, not across tenants.** Types are text / secret / number /
bool.

A secret is only read at runtime and never returned by the API; do not paste it into `AGENT.md` or
`SKILL.md`. When editing a secret, **empty means leave the current value alone**. Asset packages carry the
variable name, not the value.

## Service credentials

Tenant-level, shared by MCP and RPC. This is the only place an API key is created.

A new binding starts as "restricted to a set of endpoints, with no target chosen" — unless you click
Configure and tick targets, the key **cannot reach any endpoint**. Granting an external credential a super
agent endpoint hands over tenant management; do not select everything out of convenience.

## LLM configuration

Tenant-level: provider, model, base URL, API key, context and generation parameters. A provider preset
fills in the name, protocol and URL, but **you still have to supply the API key** — it is validated before
saving.

## Inbound connections

The MCP authentication / RPC authentication pages only hold the switch (API key or OAuth); the key-to-user
binding is done in service credentials.

## Account items are not on this page

Changing your password, switching language and signing out live in the top bar. The "reset password" on
this page is an admin resetting **another user**, not yourself.

## Two key fields with opposite semantics

A global variable's secret left empty means "keep the old value"; an LLM API key is required and empty is
rejected. Same-looking box, different behaviour.
