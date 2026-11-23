# Flame PoC — operator's manual

[中文](MANUAL.zh.md) | English

This manual is for someone who wants to run the PoC with Docker on one machine and then walk through the features. The
installation details and the design notes live in the [README](README.md); this file is only about **how to operate it**.
It does not cover production deployment, clusters, TLS or SSO — see "Known limits" at the end.

## Contents

1. [Running it in five minutes](#1-running-it-in-five-minutes)
2. [Two consoles, three accounts](#2-two-consoles-three-accounts)
3. [The one thing you must do first: connect a model](#3-the-one-thing-you-must-do-first-connect-a-model)
4. [Verify the agent](#4-verify-the-agent)
5. [The tenant side in six parts](#5-the-tenant-side-in-six-parts)
6. [The platform side in four parts](#6-the-platform-side-in-four-parts)
7. [Calling in from outside](#7-calling-in-from-outside)
8. [Data, backup, reset, upgrade](#8-data-backup-reset-upgrade)
9. [Troubleshooting](#9-troubleshooting)
10. [Known limits](#10-known-limits)

## 1. Running it in five minutes

```bash
git clone https://github.com/buffalo-xi/flame.git
cd flame
docker compose -f deploy/poc/docker-compose.yml pull     # or up -d --build to compile it here
docker compose -f deploy/poc/docker-compose.yml up -d
```

Tell "up" from "the process is still there":

```bash
docker compose -f deploy/poc/docker-compose.yml ps       # flame says healthy
docker compose -f deploy/poc/docker-compose.yml logs -f  # first run 2-3 min: gateway migrations -> SQL -> seedpoc -> supervisord
curl -fsS http://localhost:27001/healthz                 # 200
```

Open <http://localhost:27001>. The slow first start is expected: the gateway runs its Prisma migrations and nexus answers
`503` until postgres, redis and the gateway are all reachable — so `healthy` means "it can serve", not "the process is up".

| Want to | Command |
|---|---|
| follow the logs | `docker compose -f deploy/poc/docker-compose.yml logs -f flame` |
| see the six processes | `docker exec flame supervisorctl status` |
| restart (after building locally) | `docker restart flame` |
| stop, keep the data | `docker compose -f deploy/poc/docker-compose.yml down` |
| stop and wipe | `docker compose -f deploy/poc/docker-compose.yml down -v` |

## 2. Two consoles, three accounts

The login card has a switcher in its top-right corner: **Console** (tenant) and **Platform**. They are two independent
sessions that can coexist; the platform top bar's "back to console" jumps straight back when the tenant session is still
alive.

| Account | Password | Entered from | What it is |
|---|---|---|---|
| `plat` | `12345678` | Platform | platform administrator: tenants, platform users, plugins & capabilities, system settings, local/remote market |
| `primary` | `12345678` | Console, **tenant field = `t0`** | administrator inside `t0`: agents, skills, tools, workflows, runs, endpoints, assets, settings |
| `user01` | `12345678` | Platform | platform `user` role, only "my tenants", no admin pages |

The part that catches people out: **the console login asks for a tenant as well** (the schema name; the demo tenant is
`t0`). `primary` / `12345678` without `t0` will not log in.

Change a password: both console top bars have "password". `plat` is a protected account — its password cannot be changed.

## 3. The one thing you must do first: connect a model

No provider key ships in the image. Until you add one, every page works but the agent fails on the first message — the
gateway has no model to call.

In the **tenant console**: Settings → **LLM** (the first tab). The example below is **DeepSeek over the
OpenAI-compatible protocol**; for another vendor only the URL and the model identifier change. DeepSeek has no built-in
preset, so pick the generic `openai_compatible` one — that is the entry point for every OpenAI-protocol vendor.

**Step 1: add the LLM provider**

1. Pick **`openai_compatible`** in "choose a preset provider": it fills in the protocol (`openai`, read-only) and a
   placeholder URL
2. Provider name: `DeepSeek`
3. API URL: `https://api.deepseek.com`
4. API key: `sk-...` (created in the DeepSeek console)
5. Extra config (JSON): **leave empty**. This one goes into LiteLLM's `litellm_params` — **registration-time**
   parameters such as Azure's `api_version` or Vertex's `vertex_project`, not the body of each request

**Step 2: add the LLM model** (same page → add LLM model, provider = the DeepSeek one)

| Field | Value | Why |
|---|---|---|
| Model name | `deepseek-flash` | the gateway calls `openai/<model name>`, so it **must match the upstream identifier** |
| Context window | `128000` | per the vendor's docs; used for estimates and truncation |
| Gateway model name | `deepseek-flash` | the alias clients see; a request carries `<schema>:<this>`; **locked once enabled** |
| Generation params | enable `max_tokens` / `temperature` / `top_p` as needed | switch-based: only enabled keys are written to `base_params` (absent = not sent, vendor default) |
| Extra parameters (JSON) | `{"thinking": {"type": "disabled"}}` | DeepSeek syntax, see below; another vendor needs its own switch |
| Capabilities (JSON) | usually empty | e.g. `{"vision": true}` declares multimodal |

Saving pushes it into the gateway (the LiteLLM container); only then does the agent have a model.

### What `{"thinking": {"type": "disabled"}}` is for

This line is **a DeepSeek extension field, not a requirement of this system**. With another vendor, use that vendor's own
knob: `reasoning_effort` for OpenAI reasoning models, "no `thinking` field means off" for Anthropic, and Qwen / GLM and
friends each have their own; when unsure, **leave it empty** — this system only passes the field through and never
validates it, so the model works without it.

It belongs in the **model's "extra parameters"**, where it is folded into `extra_body` on every request; LiteLLM does not
validate it and passes it to the upstream as-is (the code comment reads `extra_params all go into extra_body`). So it is
an **instruction to the upstream: turn this model's reasoning output off and return the answer only**. The same JSON also
lands in `model_info` (registration metadata), but the copy that takes effect per request is the `extra_body` one.

The demo disables thinking by default for four reasons:

1. **Latency and cost**: reasoning tokens are billed as output tokens and push the first token later;
2. **Tool calling is steadier**: the agent leans on function calling (files, shell, browser). In reasoning mode a model
   often writes a long trace first, and some upstreams support tool calls less well in that mode;
3. **The console and the flows only want the answer**: thinking is rendered as a collapsible "reasoning" block in the
   console, and an LLM node in a flow usually takes the answer only — reasoning would be pure payload;
4. **Reproducibility**: the same model writes very differently with thinking on and off.

Two boundaries:

- This system only **passes it through and never validates it**. If the upstream understands it (DeepSeek's
  OpenAI-compatible endpoint uses `thinking.type` to switch reasoning mode) it takes effect; if not, it is ignored as an
  unknown field and **nothing breaks** — the only difference is whether the reply contains reasoning. If your endpoint
  explicitly rejects the parameter, clear the JSON; nothing else depends on it.
- To watch the reasoning instead, use `{"thinking": {"type": "enabled"}}` (or the vendor's reasoner model); the console
  renders it as a collapsible "reasoning" block.

Two traps:

- `LITELLM_SALT_KEY` is what encrypts provider keys inside the gateway database, so it must live as long as the volume.
  Change it and every stored key becomes undecryptable — the model stays listed and every call fails with an auth error.
- The tenant's LLM tab manages **that tenant's** providers and models; the platform has a system-level layer on top of it.
  Do not mix the two.

## 4. Verify the agent

Tenant console → Agents → `main` (preinstalled, protected) → the chat toggle in the top bar → send a message.

- It can already use files, a shell and a browser inside this container (Chromium is installed, `browserd` runs a browser
  pool). `sandboxd` runs `mode: none`, so the "sandbox" boundary is the container itself.
- Every run lands one row: **Runs → Agent history**, with status, duration, question and answer.
- The agent's settings panel has two tabs: **grants** (which skills it may use) and **tools** (tool grants and the tools
  that end up in effect).

To build your own: Agents → new → edit `AGENT.md` (source / Markdown preview) → save.

## 5. The tenant side in six parts

**5.1 Skills**: `SKILL.md` + `references/` + optional scripts. The settings panel has three tabs — **visibility**
(shared in the tenant, or private to one agent), **tool dependencies** (a skill only declares what it needs; the grant is
given on the agent side), **properties**.

**5.2 Tools**: tool catalog / plugins / custom tools. This only decides what *exists*; whether a given agent may use one
is granted in the agent's settings panel.

**5.3 Workflows**: new → drag nodes from the node panel → trial run (this run / run history) → version release and
deploy. Version operations use `release` / `deploy` / `rollback` and never "publish".

**5.4 Runs**: running / workflow history / agent history.

**5.5 Assets**: the asset market → list / delist; also installs from another tenant.

**5.6 Settings** (eleven tabs in the left column): LLM, MCP connections (outbound), RPC connections (outbound), MCP auth
(inbound), RPC auth (inbound), service credentials, users, global variables, tenant quota, workflow run parameters, agent
run parameters.

Endpoints (the "Endpoints" nav item) are **not exposed by default**. To let the outside call an agent or a workflow, you
**expose** it; taking it back is **retract**, not `revoke` — `revoke` is only for grants and credentials.

## 6. The platform side in four parts

**6.1 Tenants**: one row per tenant. Creating one lets the backend pick the next free schema (e.g. `t1`); you supply the
display name, the first administrator's name (default `primary`) and password, and optionally an owner. Each row also
carries the capability switches (exec / shell / egress / browser …) that apply to that tenant.

**6.2 Platform users**: account, role (`admin` / `user`), enabled state. A `user` only sees "my tenants".

**6.3 Plugins & capabilities**: the platform-level catalog; how much of it one tenant gets is controlled on its row in 6.1.

**6.4 System settings / local market / remote market**: system-level limits; listing policy and remote-market access.

## 7. Calling in from outside

One API key is preinstalled: `sk-poc-t0-primary`, bound to `primary` in `t0`. Send it in the `Authorization` header.
Three entry points, all on the same port:

| Endpoint | Purpose |
|---|---|
| `POST /service/mcp/t0` | MCP: `initialize`, `tools/list`, `tools/call` |
| `POST /service/rpc/t0` | RPC flavour of the tenant's own interfaces |
| `GET /healthz` | liveness and readiness (nginx → nexus `/health`) |

MCP answers with server-sent events, so `Accept` has to include `text/event-stream`. Handshake first, then list:

```bash
curl -s -X POST http://localhost:27001/service/mcp/t0 \
  -H 'Authorization: Bearer sk-poc-t0-primary' \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}'
# 200, answers event: message data: {"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"flame-mcp-server","version":"2.0"}, ...}}

curl -s -X POST http://localhost:27001/service/mcp/t0 \
  -H 'Authorization: Bearer sk-poc-t0-primary' \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
# 200, answers {"jsonrpc":"2.0","id":2,"result":{"tools":[]}}
```

`tools: []` is correct: `tools/list` **only lists exposed endpoints**, and a fresh `t0` has no endpoint with
`exposed = true`. Expose an agent or a workflow in the console first and it shows up here. Without `Authorization` the
answer is 401.

> In Windows PowerShell `curl` is an alias for `Invoke-WebRequest`; type `curl.exe`.

## 8. Data, backup, reset, upgrade

| Path in the container | Contents |
|---|---|
| `/data/pg` | the postgres cluster |
| `/data/tenants/<schema>/` | each tenant's definition files and agent workspaces |
| `/data/platform-assets/` | marketplace snapshots (belong to no tenant) |
| `/data/plugins/` | Python plugins |
| `/var/lib/flame/profiles` | Chromium profiles (per session, deleted when the session ends) |

**Backup** (confirm the volume name with `docker volume ls`; the default is `flame-poc_flame-data`):

```bash
docker volume ls | grep flame
docker run --rm -v flame-poc_flame-data:/data -v "$PWD":/backup alpine \
  tar czf /backup/flame-data.tgz -C /data .
```

**Reset**: `down -v`; the next `up -d` recreates the database, `t0` and the demo data.

**Upgrade**: replace the image and `up -d`. On boot the scripts under `nexus/platform/sql` are applied again, which carries
platform-side schema changes; but a tenant's **tables and definition-domain files are written once, when the tenant is
created**. A cross-version upgrade therefore wants `down -v` — otherwise you run new code against the old shape, and the
typical symptom is an agent history that stays empty.

**Change the code without rebuilding the image**: `powershell -File deploy/poc/build.ps1` cross-compiles and writes a
compose override, then `docker restart flame`. See the README.

## 9. Troubleshooting

| Symptom | What to do |
|---|---|
| The page does not load, `ps` shows `127.0.0.1:27001->27001/tcp` | the port is loopback-only; use `http://localhost:27001` here, or change the mapping for another machine |
| `port is already allocated` | something else owns 27001; change the left side of the mapping |
| `healthy` never arrives | the first run needs 2-3 minutes; `logs -f` shows which step it is on |
| Login works, the first message fails | no model connected, see 3 |
| The model is listed but calls fail with auth/decrypt errors | `LITELLM_SALT_KEY` changed; stored keys are undecryptable, re-enter them |
| `/service/...` answers 405 | your image is older than this manual (nginx has no `/service/` route); use a newer image or add the location |
| Agent history is always empty | the data volume predates a table/column; `down -v` and start over (8) |
| Tool calls fail, the browser crashes | usually the browser process being killed; `shm_size` is already 1 GB, check the `browserd` lines in `docker logs flame` |
| The build stops in `go build` with a refused connection to proxy.golang.org | the builder has no route to the default Go proxy; set `GOPROXY` (e.g. `https://goproxy.cn,direct`) in `deploy/poc/.env` |
| I want to query the database directly | `docker exec flame psql -h 127.0.0.1 -U postgres -d flame` (the `-h` is required: the cluster's socket lives in `/tmp`) |

## 10. Known limits

- The demo passwords are placeholders **baked into the image** (`plat` is a protected account whose password cannot be
  changed), and there is no TLS.
- With `mode: none` the agent's shell runs as container root and can read everything inside the container — including the
  database under `/data/pg` and the keys under `/etc/flame`. It must never sit on a network somebody else can reach.
- The image is amd64 only; an ARM machine runs it under emulation (compose declares `platform`).
