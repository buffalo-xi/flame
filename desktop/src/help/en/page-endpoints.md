# The Endpoints screen

An endpoint exposes an agent or a flow as an entry point callable from outside. This screen is the list of
those entry points.

## Two protocols

| Protocol | URL | Parameters |
|---|---|---|
| MCP | `/service/mcp/<schema>` | An agent declares no inputs; callers pass free-form JSON |
| RPC | `/service/rpc/<schema>` | Always mirrors the flow's start node inputs |

An entity can have one endpoint per protocol, and **the endpoint name is the entity name** — it cannot be
changed.

## Parameters cannot be typed by hand

An RPC endpoint's parameters always come from the start node: change the start node's inputs and the endpoint
follows. That is deliberate — it avoids the endpoint signature and the implementation disagreeing.

## Expose and retract

- **Expose**: external calls carrying a credential are allowed in.
- **Retract**: external calls stop immediately; the endpoint row, its input schema and its binding all stay,
  and exposing it again restores them.

Retracting is not deleting, and it is not "internal only" — it decides **whether the endpoint can be called**.

## Delete and deprecate

An exposed endpoint cannot be deleted; retract it first. Deprecating the agent or flow **retracts all of its
endpoints automatically**, so callers in production fail at once.

An RPC endpoint is managed by the flow and cannot be deleted separately.

## Credential scope decides who can call

A new service credential starts as "restricted to a set of endpoints, with no target chosen" — **unless you
open Configure and tick targets, that key cannot reach any endpoint**. Granting an external credential a super
agent endpoint hands over tenant management.

## Which endpoints can enter the tool catalog

Only a **flow's RPC endpoint** can be granted as a tool (reference `local:rpc:<name>`). MCP endpoints and
agent endpoints are for external calls only and never appear in the catalog.

## The two entry points this screen already has

The `?` next to the title explains the terminology (what an endpoint is, how to create one, how to call it),
and the "usage" drawer **generates a copyable curl and an MCP config** from the selected row's protocol and
inputs. Those two are specific to this screen, which is why it carries no page-level help topic of its own.
