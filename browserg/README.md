# browserg

[中文](README.zh.md) | English

The gateway in front of the browser pool. It exposes `/v1/browser` and an aggregated `/v1/capabilities` to nexus, routes
a session stickily to the instance that holds its Chromium, and enforces the per-tenant cap across the whole pool.

It is a **separate service and a separate module** from `../sandboxg` on purpose. The two gateways look alike, but the
browser channel disagrees with the sandbox channels on three structural points:

1. `/v1/capabilities` is a *pool* answer here (`available` = any instance, `headed_available` = all, counters summed),
   while sandboxg passes one instance's document through — and nexus derives the URL from `browser.url`.
2. The shell channel and the browser channel share the same `session_id` but must land on different pools, so the sticky
   namespace cannot be shared.
3. Capacity is not comparable: sandboxg scores by live processes, a browser's unit is a live Chromium.

Folding them together would also mean one restart drops every browser connection together with every exec route.

## Wiring

```
nexus ──browser.url──> browserg :27090 ──backends──> browserd :27091
                                                     browserd :27091 ...
```

nexus needs no code change: `browser.url` points at the gateway, and the two paths it uses (`/v1/browser`,
`/v1/capabilities`) keep their meaning. A single-instance deployment may point `browser.url` straight at one browserd —
the protocol is identical.

## Behaviour

| | |
|---|---|
| Routing | every open frame with a session key is sticky (a session-less run puts the run id in the same field); `?tenant=` is forwarded unchanged |
| Capacity | `queue × 1_000_000 + sessions × 1_000 / workers` — queueing dominates, occupancy rate breaks ties |
| Quota | `max_per_tenant` enforced here only (live counts from each instance's `/v1/sessions` + a short-lived pending count); keep browserd's own cap at 0 |
| Binding lost | the open fails with `session lost` — a browser cannot be migrated, so silently rebuilding one elsewhere would hide the loss |
| `session_empty` | clean signal: the binding is dropped without an error |

## Configure

- `sticky_ttl_sec` must exceed every instance's `idle_sec + 2×janitor_sec` (checked at run time and reported as an error
  when short) — otherwise one session can end up with two Chromiums.
- `redis` is required as soon as there are several gateway instances; memory bindings are per process. An unreachable
  redis is a startup failure, so leave `addr` empty on a machine without one.
- `backends` is the static pool, or just the seed when `discovery.type` is set (`service_name: browserd`).
- **Discovery is the default topology**: every browserd registers (`registry.enabled: true`, same `service_name` on both
  sides), so scaling the pool out or in needs no change to this file — a new instance joins on the next pull, a
  deregistered one leaves and its bindings are cleared. `backends` stays as the seed for the window before the first pull
  and for a registry outage.

## Deploy

Every browserd behind one gateway must have the **same** `workspace_root`, and that directory must be nexus's
`storage.root_dir`. Otherwise a screenshot lands on whichever machine served the session and nexus cannot read it back —
with no error anywhere.

## Verify

```bash
go test ./...     # unit tests, no browser needed
go build ./...
```

Manual check through the real protocol — point `browserclient` at the gateway instead of an instance:

```bash
cd ../browserd && go run ./cmd/browserclient -url ws://127.0.0.1:27090/v1/browser -session s1 -tenant t0 \
  -op '{"id":1,"op":"open","url":"https://example.com"}'
```
