# Flame PoC — a two-image demo

[中文](README.zh.md) | English

A demo that runs the whole product stack in one container — database, cache, API, sandbox, browser pool and the web
console — plus the official LiteLLM image as a second container for the LLM gateway. `docker compose up` is the whole
setup. This is a **proof of concept**, not a deployment: see [Not for production](#not-for-production) before putting it
anywhere reachable.

Step-by-step walkthrough (accounts, the first model, each console, calling in from outside): [`MANUAL.md`](MANUAL.md).

## What runs

Two images, one compose project:

| Container | Image | Role |
|---|---|---|
| `flame` | built from `deploy/poc/Dockerfile` | the product stack; the only published port (**27001**, loopback) |
| `flame-litellm` | `docker.litellm.ai/berriai/litellm-database:lts` | the LLM gateway; models are provisioned through it |

Inside `flame`:

| Process | Port (loopback) | Role |
|---|---|---|
| `nginx` | 27001 | the only entry point: the desktop bundle on `/`, everything else proxied to nexus |
| `nexus` | 27006 | platform + tenant API, agent runtime |
| `sandboxd` | 27081 | code execution (`mode: none` — the container *is* the sandbox) |
| `browserd` | 27091 | browser pool, one Chromium per session |
| `postgres` | 5432 | PostgreSQL **18** (from PGDG); `flame` (product data) + `litellm` (gateway data) |
| `redis` | 6379 | hard dependency of nexus (run ledger, stop signals, quota counters) |

Why the gateway is its own image: it used to be a pip install inside the flame image, which made that image carry
~400 MB of Python dependencies whose wheels had to match whatever Python the base image ships (the 3.13 upgrade would
have been gated by `uvloop` / `pydantic-core` / `orjson`). A tag is easier to reason about than a venv, and the gateway
can be upgraded without touching the product image. It has to be `litellm-database`, not plain `litellm`: the plain
image has no Prisma, and `store_model_in_db` is what makes "add a model" from the console persist.

There is **no** `sandboxg` / `browserg` and **no** consul: those exist to give several pool instances sticky routing and
one shared quota. With a single instance nexus talks to `sandboxd`, `browserd` and the gateway directly, which is the
same protocol on the same routes — one less moving part to explain.

## What an agent script runs on

| | Version | Notes |
|---|---|---|
| Python | **3.13** | Debian 13 base; `openpyxl` / `xlrd` / `pandas` / `numpy` / `requests` / `python-docx` / `beautifulsoup4` preinstalled |
| Lua | 5.4 | `lua` is symlinked to `/usr/bin/lua5.4` |

The Python packages are installed at build time, deliberately not by the agent: a docker-mode sandbox runs with
`--network none` and a read-only rootfs, so "I will just pip install it inside the script" works in this box and fails
in the real one. `docker exec <container> cat /etc/flame-versions` prints what is actually inside. Node is absent, so
the `js` language is off in `sandboxd.yaml`.

## Run it

Requirements: Docker Desktop (or Docker Engine), ~3 GB of images (flame ≈ 1.2 GB, litellm 1.73 GB), 4 GB RAM, and an
x86-64 host — the image pins `GOARCH=amd64`, and `docker-compose.yml` declares `platform: linux/amd64` so an ARM machine
runs it under emulation instead of failing with `exec format error`.

Get the files first — every command below is relative to the repository root, and the compose file mounts `./litellm.yaml`
from its own directory (and, when building, uses the repository root as its context), so the folder has to be whole:

```bash
git clone https://github.com/buffalo-xi/flame.git
cd flame
```

Then either **pull the published image** (no Go, no Node, no build — this is the normal way to try it):

```bash
docker compose -f deploy/poc/docker-compose.yml pull
docker compose -f deploy/poc/docker-compose.yml up -d
```

or **build it yourself** (the build runs inside the image's builder stages, so the host needs no toolchain — but it does
need network access to a Go module proxy and an npm registry; 5–10 minutes, and the first run of each is the slow one):

```bash
docker compose -f deploy/poc/docker-compose.yml up -d --build
```

Either way, open <http://localhost:27001>. One port serves **two consoles with two different accounts**, which is the part
that catches people out:

- **`primary` / `12345678`** — the administrator **inside** `t0`: the tenant console (agents, skills, workflows, chat, and
  that tenant's own providers / models / connections). It is a tenant account, so it sees no other tenant and none of the
  platform's tenant or user administration.
- **`plat` / `12345678`** — the **platform administrator**: creates tenants, adds LLM providers and models. **Start here.**
  The platform console lists the demo tenant `t0`, and that list is how you switch into it.
- **`user01` / `12345678`** — a platform account with role `user` that owns `t0`; sign in with it to see the non-admin
  platform view (the tenants it owns, no admin pages).

Three settings in `docker-compose.yml` are easy to overlook and not optional:

- `shm_size: "1gb"` on `flame` — Chromium uses `/dev/shm`; with Docker's default 64 MB pages crash at random, which
  looks like "the browser tool is broken".
- `flame-data:/data` — keep the volume if tenant data should survive `compose down`; drop it for a throwaway run.
- `LITELLM_SALT_KEY` — has to stay stable for the life of the volume. It encrypts provider keys in the gateway database:
  change it and every stored key becomes undecryptable (the model stays listed, every chat fails with an auth error).

First start takes 2–3 minutes: the gateway runs its Prisma migrations, nexus waits for it, and the sandbox language
probe runs once. Follow along with:

```bash
docker compose -f deploy/poc/docker-compose.yml logs -f
```

`docker compose ... ps` reports `flame` healthy only when nginx is up **and** nexus is ready, and nexus answers `503`
until postgres, redis and the gateway are all reachable — so "healthy" is a truthful signal, not just "the process
started".

## First chat in three steps

The `flame` container seeds a working environment on every boot (`nexus/cmd/seedpoc`, idempotent), so there is nothing
to create by hand:

| Account | Password | What it is |
|---|---|---|
| `plat` | `12345678` | platform administrator — the account the console is entered with |
| `user01` | `12345678` | platform `user` role, owner of the demo tenant |
| `primary` | `12345678` | administrator *inside* the `t0` tenant (the account provisioning hands over) |

The tenant `t0` ("Demo tenant") already exists with the preinstalled `main` agent, the four built-in skills and every
capability switched on. An apikey `sk-poc-t0-primary` is bound to `primary`, so `/service/mcp/t0` and `/service/rpc/t0`
accept `Authorization: Bearer sk-poc-t0-primary`. Note what that does and does not mean: the credential is ready, but the
tenant has no *exposed* endpoint yet, so `tools/list` comes back empty until you expose one (endpoints only list what
has `exposed = true`).

1. **Sign in.** `plat` / `12345678` enters the platform console, which lists `t0`; entering that tenant uses
   `primary` / `12345678`.
2. **Add a model.** Settings → LLM: add a provider (an OpenAI-compatible base URL + key works for most vendors), then
   add a model on top of it. This is what provisions the model into LiteLLM — without it the console works but every
   answer fails, because there is no gateway model to call.
3. **Chat.** Open the `main` agent and send a message. It can already use files, shell and a browser in this box.

No provider key ships in the image: nothing sensitive is baked in, and step 2 is where you supply yours. Creating more
tenants from the console still works exactly as before — `t0` is just a demo that is already there.

## Change the code without rebuilding the image

The image contains a working build (it compiles in a builder stage), so this is optional — it is the
"edit, restart, see it" loop:

```powershell
powershell -File deploy/poc/build.ps1     # cross-compiles linux/amd64 binaries + builds the desktop bundle into out/
docker restart flame                      # picks them up
```

`build.ps1` writes `deploy/poc/out/{bin,dist}` plus `deploy/poc/out/compose-mounts.yml` (both gitignored) — a compose
override that mounts those two directories into the `flame` container — and prints the command:

```bash
docker compose -f deploy/poc/docker-compose.yml -f deploy/poc/out/compose-mounts.yml up -d
```

Those binaries are Linux binaries — they do not run on the host, that is what they are for. The gateway container is
untouched by this loop; it runs the published image.

To also run one service's source under the debugger, keep using the module-level setup (`go run`, the Vite dev server);
this container is for shaking out the integrated thing.

## Data, volumes, reset

| Path in the container | Contents |
|---|---|
| `/data/pg` | the postgres cluster |
| `/data/tenants/<schema>/` | each tenant's definition files and agent workspaces |
| `/data/platform-assets/` | marketplace snapshots (belong to no tenant) |
| `/data/plugins/` | Python plugins |
| `/var/lib/flame/profiles` | Chromium profiles (per session, deleted when the session ends) |

`/data` is one volume shared by nexus, sandboxd, browserd and nginx **at the same absolute path** — that shared path is
what makes "the sandbox writes a file, the tenant downloads it" work, and what makes a browser screenshot land somewhere
the model can read it back.

Want to browse those files from the host? Put a bind mount in a compose override file next to `docker-compose.yml`,
but keep postgres on a real volume (9p/CIFS filesystems and postgres do not get along):

```yaml
# docker-compose.override.yml
services:
  flame:
    volumes:
      - flame-pg:/data/pg
      - d:/flame-data:/data
```

Reset:

```bash
docker compose -f deploy/poc/docker-compose.yml down        # stops both containers, keeps the volume
docker compose -f deploy/poc/docker-compose.yml down -v     # drops the volume as well (all data is gone)
```

Upgrading from an older image: the platform SQL under `nexus/platform/sql` is re-applied on every boot, but a tenant's
**table definitions and definition-domain files** (`agents/main/AGENT.md`, the four protected skills) are written once, when
the tenant is created. A kept volume therefore runs the old shape under new code — `down -v` (a fresh volume) is the honest
upgrade path for a demo box.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| page loads, every API call is 503 | a hard dependency is not up yet — `docker compose ... logs -f flame \| tail -50`, look for `readiness` |
| login rejected | the admin seed did not run (check the bootstrap output in the logs) or the password is not `12345678` |
| bootstrap stops at `check the preinstalled assets of t0` | the tenant lost `main` or one of the four protected skills (only reachable by editing the database by hand); start from a fresh volume, or put the row back |
| console works, chat answers with a model error | no provider/model configured (step 2 above) |
| browser tool: "browser unavailable" | Chromium could not start — most often `shm_size` was removed from the compose file |
| `flame-litellm` restarting in a loop | its database is not there yet: it starts before the flame container finished initialising. Check `docker compose ... logs flame-litellm` — a `pg_hba` rejection means the trust line for the compose network is missing, a connection refusal means the cluster is still starting |
| models are listed, every chat fails with an auth error | `LITELLM_SALT_KEY` changed: the provider keys stored in the gateway database can no longer be decrypted. Restore the old value, or re-enter the provider key |
| bootstrap says `the data volume was created by postgres 17, this image ships 18` | the `/data` volume predates the PostgreSQL 18 base image. A data directory cannot be opened by another major — start from a fresh volume, or run `pg_upgrade` against the old one |
| `port is already allocated` | something else owns 27001; change the left side of the mapping in `docker-compose.yml` |
| build fails in `go build` with `connect: connection refused` to proxy.golang.org | the builder has no route to the default Go module proxy — set `GOPROXY` (e.g. `https://goproxy.cn,direct`) in `deploy/poc/.env`, or pass `--build-arg GOPROXY=...` |

`docker exec flame supervisorctl status` shows the six processes inside the product container and their restart counts.
The gateway has no supervisor: `docker compose ... ps` and its own logs are the whole story.

## Not for production

- **`plat` / `12345678` is baked into the image**, the seeded `user01` / `primary` accounts share that password, and the
  platform administrator is a protected account.
- `sandboxd` runs in `mode: none`: the agent's shell is an ordinary process in this container with read access to
  `/data`, i.e. to every tenant's files. Fine for one user on a laptop, unacceptable for anything multi-tenant.
- Service-to-service tokens are empty, and JWT / value / gateway keys are PoC values. Postgres now listens beyond
  loopback and trusts the whole compose network (that is what lets the gateway connect) — nothing is published to the
  host, but any container added to this compose project can read both databases.
- Both containers share `LITELLM_MASTER_KEY` and `LITELLM_SALT_KEY`; whoever gets the environment of either one gets
  the gateway's control plane.
- Two containers are still one failure domain in practice: a crash of `flame` takes the database and the API down
  together, and the gateway is useless without them.

Treat it as "a demo you can hand to somebody", not "an installation". For a real deployment, the same services run as
separate containers with isolation turned on and the gateways in front.
