# Flame deployment runbook

[中文](deployment-runbook.zh.md) | English

> Status: the operational companion to [`deployment.md`](deployment.md). That file explains *why*; this one lists the
> exact steps, files and checks. `deploy/prod/` is still a **specification** — the compose files, units and scripts below
> are the shape to create, not files that exist in the repository today.
>
> Terminology follows [`terminology.md`](terminology.md): assets are `list` / `delist`, endpoints `expose` / `retract`,
> versions `release` / `deploy` / `rollback`. Reverting a deployment is "switch back to the previous release", which is
> not the version-domain `rollback`.

---

## 1. Pick a tier

| Tier | Shape | Ports to open | When |
|---|---|---|---|
| 1 | every container + the native executors on one host | `27001` (+`27443`) out; `127.0.0.1:8500` local | development, small installs |
| 2 | first host runs every container, other hosts only add `sandboxd` / `browserd` | `8500` for registration; `27081` / `27091` towards the executors | the default production shape |
| 3 | containers split across hosts (nexus / gateways / data plane) | everything in §4 | HA or heavy load |

Tier 2 opens **no** data-plane port: executors never talk to PostgreSQL or Redis. Only tier 3 does.

## 2. Host prerequisites

- Linux x86_64, kernel ≥ 5.10; `nsjail` isolation additionally needs cgroup v2 and its own rootfs (built per image
  generation, §5.3)
- Docker Engine ≥ 24 with Compose v2
- NTP on every node (JWT expiry, log ordering and sticky TTLs all assume a sane clock)
- the same shared directory mounted at `/srv/flame/data` on every node (NFS / CephFS / multi-attach volume)
- one dedicated system user `flame`; the native executors run as it
- rough sizing: 4C/8G carries a single-host install; start production at 8C/16G, and reserve 1–2 GB per concurrent
  browser instance

## 3. Layout

```
/opt/flame/
├─ current -> releases/<ver>
├─ releases/<ver>/
│  ├─ bin/{nexus,seedpoc,sandboxd,codehost,browserd,browserhost,backfillversions}
│  ├─ static/            # nexus static assets
│  ├─ web/               # desktop build output
│  ├─ sql/               # platform SQL, applied in filename order
│  ├─ compose.yaml
│  └─ compose.multi-host.yaml
└─ data -> /srv/flame/data

/etc/flame/
├─ flame.env             # secrets, addresses, switches (chmod 600)
├─ nexus/config.yaml
├─ sandboxd/config.yaml
├─ browserd/config.yaml
├─ sandboxg/config.yaml
└─ browserg/config.yaml

/srv/flame/data/{tenants,platform-assets,plugins}
/srv/flame/run/tmp/      # sandboxd tmp_dir, host-local, exclusive
```

`nexus` takes **no `-config` flag**: it reads `config.yaml` from its working directory, so its container's working
directory is `/etc/flame/nexus` and `server.static_root` must be the absolute `/opt/flame/static`. The other four
services accept `-config`.

## 4. Ports and dependencies

| Client | Dependency | Port | Tier 1 | Tier 2 | Tier 3 allow-list |
|---|---|---|---|---|---|
| users / desktop | nginx | 27001 (+27443) | public | public | public |
| nginx | nexus | 27006 | `flame-net` | `flame-net` | nginx node |
| nexus | sandboxg / browserg | 27080 / 27090 | `flame-net` | `flame-net` | nexus node |
| nexus | PostgreSQL / Redis / LiteLLM | 5432 / 6379 / 4000 | `flame-net` | `flame-net` | every nexus node |
| LiteLLM | PostgreSQL | 5432 | `flame-net` | `flame-net` | LiteLLM node |
| sandboxg / browserg | Redis / Consul | 6379 / 8500 | `flame-net` | `flame-net` | every gateway node |
| sandboxd / browserd | Consul registration | 8500 | loopback | first host intranet | every executor node |
| Consul | executor health checks | 27081 / 27091 | loopback / intranet | intranet | Consul node → every executor |
| executors | PostgreSQL / Redis | — | none | none | none |

Consul with more than one server additionally needs `8300` (server RPC) and `8301` (LAN serf, TCP+UDP) between the
servers, and a unique `-node` name per server; a single server publishes neither.

## 5. Configuration

### 5.1 `/etc/flame/flame.env` (0600)

```bash
FLAME_VERSION=1.4.0
FLAME_BIND_IP=127.0.0.1            # tiers 2/3: this host's intranet IP
COMPOSE_FILE=compose.yaml          # tier 3: compose.yaml:compose.multi-host.yaml

POSTGRES_PASSWORD=<random>
REDIS_PASSWORD=<random>
JWT_SECRET=<openssl rand -base64 48>
FLAME_VALUE_KEY=<openssl rand -base64 48>
LITELLM_MASTER_KEY=sk-<random>
SANDBOX_TOKEN=<random>
BROWSER_TOKEN=<random>
SEED_PASSWORD=<random>

FLAME_DB_HOST=postgres
FLAME_REDIS_ADDR=redis:6379
FLAME_LITELLM_HOST=litellm
FLAME_SANDBOXG_HOST=sandboxg
FLAME_BROWSERG_HOST=browserg
```

### 5.2 `/etc/flame/nexus/config.yaml` (full `${VAR}` expansion)

```yaml
server:
  host: 0.0.0.0
  port: 27006
  static_root: /opt/flame/static
database:
  type: postgres
  host: "${FLAME_DB_HOST}"
  port: 5432
  username: flame
  password: "${POSTGRES_PASSWORD}"
  database: flame
  platform_schema: platform
redis:
  mode: single
  addrs: ["${FLAME_REDIS_ADDR}"]
  password: "${REDIS_PASSWORD}"
  db: 0
jwt:
  secret_key: "${JWT_SECRET}"
  token_expiry: 600
  issuer: "nexus@flame"
security:
  value_key: "${FLAME_VALUE_KEY}"
registry:
  enabled: false
litellm:
  enabled: true
  url: "http://${FLAME_LITELLM_HOST}:4000"
  master_key: "${LITELLM_MASTER_KEY}"
  database: litellm
sandbox:
  url: "ws://${FLAME_SANDBOXG_HOST}:27080"
  token: "${SANDBOX_TOKEN}"
browser:
  url: "ws://${FLAME_BROWSERG_HOST}:27090"
  token: "${BROWSER_TOKEN}"
storage:
  root_dir: /srv/flame/data
  driver: local
download:
  x_accel_prefix: /__files
```

Unset `${VAR}` expands to an empty string and makes the service refuse to start — deliberately. Every variable above
must be present in `flame.env`.

### 5.3 `sandboxd` / `browserd` (rendered, no `${VAR}`)

```yaml
# /etc/flame/sandboxd/config.yaml
listen: "0.0.0.0:27081"
token: "${SANDBOX_TOKEN}"
mode: docker
allowed_mount_roots:
  - "/srv/flame/data"
tmp_dir: "/srv/flame/run/tmp"
docker:
  image: flame/sandbox:<ver>
  network: bridge
  read_only: true
registry:
  enabled: true
  type: consul
  address: "<first host intranet IP>:8500"
  service_name: sandboxd
  health_check_path: /healthz
  # service_addr is optional: empty derives it from listen.
  # Never write 127.0.0.1 with a remote registry: startup refuses.
```

```yaml
# /etc/flame/browserd/config.yaml
listen: "0.0.0.0:27091"
token: "${BROWSER_TOKEN}"
workspace_root: "/srv/flame/data"
browser:
  # Nested, not top level: a flat `profile_root` is not a known key and is ignored,
  # and the default (`run/browserd/profiles`, relative to the cwd) fills a disk nobody watches.
  # Per session, and deliberately outside the tenant tree - it holds cookies for that session only.
  profile_root: "/srv/browser/profiles"
registry:
  enabled: true
  type: consul
  address: "<first host intranet IP>:8500"
  service_name: browserd
```

Render them once (they live in `/etc/flame`, which upgrades do not overwrite). The templates use the same `${VAR}` form
nexus does — `envsubst` with no list would also eat nginx-style `$` in the file, so name the variables:

```bash
set -a; . /etc/flame/flame.env; set +a
envsubst '${SANDBOX_TOKEN} ${BROWSER_TOKEN} ${REDIS_PASSWORD}' \
  < /opt/flame/current/templates/sandboxd.yaml > /etc/flame/sandboxd/config.yaml
```

These four services read their yaml literally — `${VAR}` left in place is not expanded at runtime, so an unrendered
template starts with the placeholder itself as the token and every call is rejected.

#### The nsjail rootfs is built, one per image generation

Only `mode: nsjail` needs one — docker mode mounts the image directly. The image *is* the contract (interpreters,
libraries, `codehost`), so export it instead of hand-building a rootfs:

```bash
# Build into a versioned directory: a revert needs the old one, so nothing is overwritten in place.
R=/opt/flame/rootfs/<ver>
docker create --name flame-rootfs flame/sandbox:<ver> true
install -d "$R"
docker export flame-rootfs | tar -x -C "$R"
docker rm flame-rootfs

# `docker export` carries no device nodes, and codehost opens /dev/null for every child. Without these the jail starts
# and then fails the first exec with: codehost: start python3: open /dev/null: no such file or directory
install -d "$R/dev"
mknod -m 666 "$R/dev/null"    c 1 3
mknod -m 666 "$R/dev/zero"    c 1 5
mknod -m 666 "$R/dev/random"  c 1 8
mknod -m 666 "$R/dev/urandom" c 1 9
mknod -m 666 "$R/dev/tty"     c 5 0

# Prove it before pointing sandboxd at it: a broken interpreter path only shows up per request, as "unavailable".
nsjail -Mo --chroot "$R" -- /usr/bin/python3 -V
nsjail -Mo --chroot "$R" -- /usr/local/bin/lua -v
```

```yaml
mode: nsjail
nsjail:
  binary: nsjail                  # /usr/local/bin/nsjail when built from source
  rootfs: /opt/flame/rootfs/<ver>
  uid: 99999
  gid: 99999
  disable_network: true
  read_only_rootfs: true          # the tree is mounted read-only; nothing is repaired at runtime
  cgroup_version: auto
```

- **One rootfs per image generation.** Swapping `docker.image` without rebuilding the rootfs leaves nsjail running the old
  interpreters while docker mode runs the new ones, and nothing reports the difference.
- **`/usr/local/bin/lua` is an absolute symlink to `/usr/bin/lua5.4`.** Keep both halves; with only the target (or only the
  link) the language is reported unavailable at startup, with `exit 255` from nsjail and no hint about the cause.
- **Ubuntu 26.04 does not package nsjail.** Build it from source (`bison flex libprotobuf-dev protobuf-compiler
  libnl-3-dev libnl-route-3-dev libcap-dev pkg-config`) and install to `/usr/local/bin/nsjail`.
- Budget ~600 MB per rootfs and keep the previous generation until the new one has taken traffic.

Registration rules worth repeating:

- the advertised port always follows `listen`; `service_port` is gone (an old value logs a warning and is ignored);
- the service ID defaults to `<name>-<address>-<port>` and is unique per host, so nothing needs configuring;
- `service_addr` may be left empty: a concrete `listen` address is used as-is, a wildcard bind falls back to
  `advertise_iface`, then the hostname, then the first physical NIC. A failed derivation, or a loopback result while the
  registry is remote, refuses to start.

### 5.4 `sandboxg` / `browserg`

```yaml
# /etc/flame/sandboxg/config.yaml
listen: ":27080"
sandbox_token: "${SANDBOX_TOKEN}"
redis: { addr: "redis:6379", password: "${REDIS_PASSWORD}", db: 0 }
sticky_ttl_sec: 2100
discovery:
  type: consul
  address: "consul:8500"          # tier 3: the first host's intranet IP
  service_name: sandboxd
  interval_sec: 5
backends: []                      # never seed 127.0.0.1 here
```

`browserg` is the same shape: port `27090`, `browser_token`, `max_per_tenant: 1`, `service_name: browserd`.

## 6. Compose layout

`compose.yaml` is the only place service definitions live; `compose.multi-host.yaml` carries `ports` only and is loaded
in tier 3 alone (a Compose `ports` list is *merged*, not replaced, so each port has exactly one home).

```yaml
services:
  consul:
    image: hashicorp/consul:1.20
    command: ["agent", "-server", "-bootstrap-expect=1", "-client=0.0.0.0", "-data-dir=/consul/data"]
    volumes: ["flame-consul:/consul/data"]
    ports: ["${FLAME_BIND_IP:-127.0.0.1}:8500:8500"]

  postgres:
    image: postgres:16
    environment:
      POSTGRES_PASSWORD: "${POSTGRES_PASSWORD}"
      POSTGRES_DB: flame
    volumes:
      - flame-pg:/var/lib/postgresql/data
      - /srv/flame/pg-init:/docker-entrypoint-initdb.d:ro

  redis:
    image: redis:7-alpine
    command: ["redis-server", "--requirepass", "${REDIS_PASSWORD}", "--appendonly", "yes"]
    volumes: ["flame-redis:/data"]

  litellm:
    image: ghcr.io/berriai/litellm-database:main-latest
    environment:
      DATABASE_URL: "postgresql://flame:${POSTGRES_PASSWORD}@postgres:5432/litellm"
      LITELLM_MASTER_KEY: "${LITELLM_MASTER_KEY}"

  nexus:
    working_dir: /etc/flame/nexus          # nexus reads ./config.yaml from here
    volumes:
      - ./bin:/opt/flame/bin:ro
      - ./static:/opt/flame/static:ro
      - /etc/flame/nexus:/etc/flame/nexus:ro
      - /srv/flame/data:/srv/flame/data

  web:
    ports: ["27001:80", "27443:443"]
    # downloads are X-Accel-Redirect (`download.x_accel_prefix`): nginx sends the bytes itself,
    # so it needs the same tree nexus reads — `location /__files/ { alias /srv/flame/data/; }`
    volumes: ["/srv/flame/data:/srv/flame/data:ro"]
```

`/srv/flame/pg-init/00-roles.sql` runs once, on an empty data volume. The image runs it with `ON_ERROR_STOP=1`, and
`POSTGRES_DB: flame` has already created that database — so it is handed over, not created again:

```sql
CREATE ROLE flame LOGIN PASSWORD 'the-same-as-POSTGRES_PASSWORD';
ALTER DATABASE flame OWNER TO flame;      -- created by POSTGRES_DB, not by this script
CREATE DATABASE litellm OWNER flame;
```

`compose.multi-host.yaml`, tier 3 only:

```yaml
services:
  postgres: { ports: ["${FLAME_BIND_IP:?FLAME_BIND_IP must be the intranet IP}:5432:5432"] }
  redis:    { ports: ["${FLAME_BIND_IP:?FLAME_BIND_IP must be the intranet IP}:6379:6379"] }
  litellm:  { ports: ["${FLAME_BIND_IP:?FLAME_BIND_IP must be the intranet IP}:4000:4000"] }
  # only when nginx / nexus are on different hosts
  # nexus:  { ports: ["${FLAME_BIND_IP:?set FLAME_BIND_IP}:27006:27006"] }
  # only when nexus is on a different host from the gateways
  # sandboxg: { ports: ["${FLAME_BIND_IP:?set FLAME_BIND_IP}:27080:27080"] }
  # browserg: { ports: ["${FLAME_BIND_IP:?set FLAME_BIND_IP}:27090:27090"] }
```

Wrapper so tier 1 and tier 3 use the same command (`/usr/local/bin/flame`):

```bash
#!/usr/bin/env bash
set -euo pipefail
set -a; . /etc/flame/flame.env; set +a
cd /opt/flame/current
IFS=: read -r -a files <<< "${COMPOSE_FILE:-compose.yaml}"
args=(); for f in "${files[@]}"; do args+=(-f "$f"); done
exec docker compose "${args[@]}" "$@"
```

nginx (inside `web`):

```nginx
location = /health { proxy_pass http://nexus:27006/health; }
location /__files/ { internal; alias /srv/flame/data/; }
location /        { proxy_pass http://nexus:27006; }
```

## 7. First boot

```bash
# 1) directories and shared storage
install -d -o flame -g flame /srv/flame/data/{tenants,platform-assets,plugins} /srv/flame/run/tmp
install -d /etc/flame/{nexus,sandboxd,browserd,sandboxg,browserg}
install -m 600 /dev/null /etc/flame/flame.env     # fill in §5.1

# 2) release and the current symlink
tar -C /opt/flame/releases -xzf flame-<ver>.tar.gz
ln -sfn /opt/flame/releases/<ver> /opt/flame/current

# 3) render the four rendered configs (§5.3 / §5.4), one envsubst per template
#    there is no -check flag: a bad config shows up as a startup error, so start the service and read the log

# 4) data plane first
flame up -d consul postgres redis litellm

# 5) platform SQL, filename order, idempotent
for f in /opt/flame/current/sql/*.sql; do
  flame exec -T -e PGPASSWORD="$POSTGRES_PASSWORD" postgres psql -U flame -d flame -v ON_ERROR_STOP=1 -f - < "$f"
done

# 6) seed (never keep the default demo password in production)
flame run --rm --entrypoint /opt/flame/bin/seedpoc nexus \
  -config /etc/flame/nexus/config.yaml -schema t0 -password "$SEED_PASSWORD"

# 7) application plane
flame up -d
flame ps
```

Executors are systemd units on the host (`flame-sandboxd.service`):

```ini
[Unit]
Description=flame sandboxd
After=network-online.target docker.service
Requires=docker.service

[Service]
User=flame
EnvironmentFile=-/etc/flame/flame.env
ExecStart=/opt/flame/current/bin/sandboxd -config /etc/flame/sandboxd/config.yaml
Restart=always
RestartSec=3
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```

`flame-browserd.service` is identical with the browserd binary and config.

## 8. Evolution path: single host → multi-host

Tier 1 is the starting point; every step below is **additive** — nothing is reinstalled. The only step that moves data is
the shared-storage change that 8.2 and 8.3 depend on.

| You want | Go to | What it costs |
|---|---|---|
| more sandbox or browser capacity | 8.1 | one host, two ports; no gateway edit |
| more application throughput, or nexus is the single point | 8.2 (+ storage) | five ports on the intranet, shared storage, matching JWT / value keys |
| the gateway is the single point | 8.3 (+ storage) | `6379` / `8500` on the intranet, a load balancer in front of nexus |

Every step has the same three-part shape: **change the first host → prepare the new host → verify**. A step that changes
a `ports` entry always recreates that container (`restart` is not enough); services whose definition did not change are
left alone.

### 8.1 Add an executor node (tier 1 → 2)

First host — stop keeping Consul on loopback:

```bash
# 1) flame.env: FLAME_BIND_IP=<this host's intranet IP>
# 2) re-create consul so the new port binding applies (the volume keeps the catalog)
flame up -d consul
# 3) prove it is reachable from the intranet before touching the new node
curl -s "http://<first host intranet IP>:8500/v1/status/leader"
```

The gateways need no change: their discovery pulls from Consul, so the new instance joins on the next pull (≤ 5 s).

New host:

```bash
# docker, the shared mount, /opt/flame/current, the flame user
install -d /etc/flame/sandboxd
envsubst '${SANDBOX_TOKEN}' < /opt/flame/current/templates/sandboxd.yaml > /etc/flame/sandboxd/config.yaml
# registry.address -> the first host's intranet IP; service_addr left empty
systemctl enable --now flame-sandboxd
```

Firewall:

- new node → first host `8500` (registration)
- first host → new node `27081` (health check), and each gateway node → new node `27081` (traffic)

Verify the catalog grew instead of replacing the existing entry:

```bash
curl -s 'http://<first host>:8500/v1/health/service/sandboxd?passing=1'
```

Rollback: `systemctl disable --now flame-sandboxd` on the new node; the entry is deregistered, or ages out in 30 s.

### 8.2 Add a nexus replica (tier 3)

**Precondition**: `/srv/flame/data` is already the same directory on both hosts (§9, step 5). Without it the replica
serves half the files and nobody notices until a download 404s.

First host — publish everything the replica needs, because it is **not** on `flame-net`:

| Publish on the first host | Port | For |
|---|---|---|
| PostgreSQL | 5432 | platform DB + `litellm` DB |
| Redis | 6379 | hard dependency, shared instance |
| LiteLLM | 4000 | model egress |
| sandboxg / browserg | 27080 / 27090 | `sandbox.url` / `browser.url` |

```bash
# flame.env: COMPOSE_FILE=compose.yaml:compose.multi-host.yaml
# overlay: the five ports above (+ nexus:27006 if nginx is on yet another host)
flame up -d
```

New host — the same release, one container:

```bash
# 1) release dir (bin/ + static/ - nexus serves /static from it), the flame wrapper,
#    /etc/flame/flame.env with this host's FLAME_BIND_IP, the same COMPOSE_FILE and the same secrets
# 2) overlay: publish this host's own 27006 and nothing else - that is what nginx dials
#    nexus: { ports: ["${FLAME_BIND_IP:?set FLAME_BIND_IP}:27006:27006"] }
# 3) start one service, not the stack: a bare `flame up -d` would also create
#    PostgreSQL / Redis / LiteLLM / Consul here, and those belong to the first host
flame up -d nexus
```

Config: the first host's `config.yaml` with the addresses rewritten —

```yaml
server:  { host: 0.0.0.0, port: 27006, static_root: /opt/flame/static }
database: { host: "<first host intranet IP>", ... }
redis:   { addrs: ["<first host intranet IP>:6379"], ... }
litellm: { url: "http://<first host intranet IP>:4000", ... }
sandbox: { url: "ws://<first host intranet IP>:27080", ... }
browser: { url: "ws://<first host intranet IP>:27090", ... }
```

Everything else is copied **verbatim** — the database and Redis passwords, `litellm.master_key`, and `sandbox.token` /
`browser.token` (the gateways authenticate nexus with those; a freshly generated token means 401 on every sandbox call,
not a startup error). These three are the ones that look local and are not:

| Value | If it differs |
|---|---|
| `jwt.secret_key` | the replicas reject each other's tokens; users are logged out at random |
| `security.value_key` | encrypted `global_vars` cannot be decrypted; empty derives it from `jwt.secret_key` |
| `storage.root_dir` + mount | files come and go, downloads 404 |

Do **not** bring the replica up through the image entrypoint — that is the psql DDL loop — and do not run `seedpoc` there;
`nexus` itself has no migrate subcommand. PostgreSQL / Redis / LiteLLM do not belong on this host either.

If you also enable `registry` on the replica, give each host its **own bare IP** in `registry.service_addr`. The service
ID is derived from that address (`<name>-<addr>-<port>`), the default is `http://localhost`, and the health check URL is
assembled as `http://` plus that value — so two hosts on one yaml derive the same ID, displace each other in Consul, and
probe a malformed URL. Leaving registration off is fine: nginx addresses the replica directly.

nginx (or an external LB) must list both addresses; until it does, the replica receives no traffic. The `proxy_pass
http://nexus:27006` from §6 names a Compose service and can never reach the second host — replace it with an explicit
upstream:

```nginx
upstream nexus_app {
    server <first host intranet IP>:27006 max_fails=2 fail_timeout=10s;
    server <replica intranet IP>:27006  max_fails=2 fail_timeout=10s;
}
location / { proxy_pass http://nexus_app; }
```

Add the entry only once `curl -s http://<replica intranet IP>:27006/health` answers 200. nginx does not poll `/health`
on its own, and readiness counts a missing sandbox capability snapshot as not ready — so a replica whose `sandbox.url` or
token is wrong answers 503 there, and adding it first takes half the traffic down with it.

Firewall: the nginx node (or the LB) → the replica on `27006`; that is the only inbound rule the replica needs, because
nothing on the first host dials it.

Capacity: each nexus opens a fixed 25 database connections, and PostgreSQL defaults to 100 in total with LiteLLM sharing
the same instance — a third or fourth replica needs `max_connections` raised first, not after the 503s.

Rollback: remove the replica from the upstream and stop it — the first host keeps running. Closing the ports again means
editing the overlay and running `flame up -d` (five containers recreated); the replica's own `27006` is dropped from its
overlay with `flame up -d nexus` there.

### 8.3 Add a gateway (tier 3)

First host: publish `6379` (a remote gateway keeps its sticky bindings in Redis) and `8500` (it reads Consul). The new
gateway's own `27080` / `27090` must be reachable from nexus.

New host:

```yaml
listen: ":27080"
redis:  { addr: "<first host intranet IP>:6379", password: "...", db: 0 }
discovery: { type: consul, address: "<first host intranet IP>:8500", service_name: sandboxd, interval_sec: 5 }
backends: []
```

Same release and `flame.env` as the replica in §8.2, then `flame up -d sandboxg` — a bare `flame up -d` would bring the
whole stack up on this host too. Publish `27080` on this host's intranet IP and open nexus node → this host; the token
here is the one nexus sends (`sandbox.token`), not a new one.

Then point nexus at it (`sandbox.url` / `browser.url`). With more than one gateway, nexus still has a single URL per
channel — the config holds one string, not a list — so put an LB in front, or keep one gateway per channel.

Rollback: point nexus back and restart it (the URL is read at startup), then stop the new gateway.

### 8.4 What every step shares

- **Only changed services are recreated.** `flame up -d` compares a per-service config hash; publishing five ports
  recreates exactly those five containers. `restart` never applies a new `ports` binding.
- **Volumes and the network survive.** `flame-pg` / `flame-redis` / `flame-consul` and `flame-net` are untouched;
  container IPs may change, service names do not.
- **Expect a short 503** while PostgreSQL / Redis are recreated — do it in a low-traffic window and apply the firewall
  rules in the same slot.
- **Two jobs run once per replica.** Startup cleanup (`runs/` older than 24 h, orphan snapshots older than 1 h) and the
  plugin-catalog refresh take no distributed lock; they are safe because of those thresholds, so do not shorten them —
  that would let one host delete what another is still using.
- **`storage.driver: s3` does not replace the shared mount.** It covers the consumers behind the storage interface only;
  the custom-tool store and the file guards still read `storage.root_dir`, and the executors mount host paths
  (`allowed_mount_roots`, `workspace_root`).
- **Start only what belongs on the host.** `flame up -d` applies the whole compose file; on a host that adds one replica
  or one gateway, name the service (`flame up -d nexus`) — a bare call creates a second PostgreSQL / Redis / Consul.
- **Every new host needs the shared mount and its own binary/config copy**; nothing is fetched at runtime.
- **Rollback cost grows with the step**: executor ≈ zero, replica = upstream edit, gateway = a nexus restart.

## 9. Split containers (tier 3)

This is still not a reinstall: containers, data and accounts stay. The one thing that genuinely has to move is
**storage** — a tier-1 install usually has `/srv/flame/data` on local disk, and every host that runs nexus, browserd or
nginx must see the same directory (nginx sends downloads itself, through `X-Accel-Redirect`). Budget a stop-the-world
window for that copy; everything else is a config edit plus a restart.

1. `flame.env`: `FLAME_BIND_IP=<this host's intranet IP>`, `COMPOSE_FILE=compose.yaml:compose.multi-host.yaml`.
2. `compose.multi-host.yaml`: keep only the ports that really cross a host boundary.
3. `flame.env`: point `FLAME_DB_HOST` / `FLAME_REDIS_ADDR` / `FLAME_LITELLM_HOST` at the first host.
4. Open the ports in §4; set `discovery.address` in both gateways to the first host.
5. The shared directory must be the same one on every host — a same-named local directory fails as "files come and go".

Applying the change: `flame up -d` recreates **only the services whose definition changed** (Compose compares a config
hash per service), so publishing 5432/6379/4000/27080/27090 recreates exactly those five and leaves the rest alone; a
changed port always means a new container, never a `restart`. Volumes, the network and the 503 window behave as §8.4
says.

Adding a replica is a separate, heavier step with its own checklist: §8.2.

## 10. Upgrade and revert

```bash
# upgrade
tar -C /opt/flame/releases -xzf flame-<new>.tar.gz
for f in /opt/flame/releases/<new>/sql/*.sql; do
  flame exec -T -e PGPASSWORD="$POSTGRES_PASSWORD" postgres psql -U flame -d flame -v ON_ERROR_STOP=1 -f - < "$f"
done
ln -sfn /opt/flame/releases/<new> /opt/flame/current
flame up -d --force-recreate nexus
systemctl restart flame-sandboxd flame-browserd

# revert to the previous release (not the version-domain "rollback")
ln -sfn /opt/flame/releases/<old> /opt/flame/current
flame up -d --force-recreate nexus
```

`--force-recreate` is not optional: `./bin` and `./static` are bind mounts whose source is the `current` symlink, and
Docker resolves it once, when the container is created. The declared path is identical before and after the switch, so a
plain `flame up -d` sees no change and leaves the old container running the old binary. The systemd units re-exec the
path on every start, which is why `restart` is enough for them.

`mode: nsjail` adds one step: a rootfs belongs to an image generation (§5.3), so build the new one, point `rootfs:` at
it, then restart `flame-sandboxd`. Skipping it leaves the jail on the previous interpreters while docker mode — if that is
mixed in — runs the new image. Keep the old rootfs until the new one has taken traffic; deleting it makes the revert
impossible without rebuilding.

Constraint: SQL is additive only (new tables, new columns). A destructive change makes the old code run on a new schema,
and a revert stops being safe. Take a database dump before every migration.

With a replica (§8.2) the SQL runs once, but the release steps run on every host: each one has its own
`/opt/flame/current`, so `ln -sfn` and `flame up -d --force-recreate nexus` are repeated there. Upgrade one host at a time
and keep nginx pointed at the host already upgraded, not at one still on the old release.

## 11. Backup

| Object | Method | Note |
|---|---|---|
| PostgreSQL | `pg_dump -Fc flame` and `litellm`, daily | mandatory before a migration |
| `/srv/flame/data` | storage snapshot or rsync | tenant files, assets, plugins |
| `/etc/flame` | encrypted archive | holds every secret; losing it means re-issuing all JWTs |
| `flame-consul` volume | not backed up | the catalog is rebuilt by periodic re-registration |
| `flame-redis` volume | not backed up | losing it only drops sticky bindings (`session lost`) |

## 12. Security baseline

- publish only `27001` (+`27443`); everything else stays on loopback or the intranet
- mount data directories, never the docker socket; only the host-side `sandboxd` may talk to the daemon
- the desktop bundle and the API share one origin; no CORS relaxation is needed
- Consul without ACLs is a writable control plane: loopback, or intranet + firewall allow-list, or ACL + TLS
  (tokens can only arrive through `CONSUL_HTTP_TOKEN`; none of the four service configs exposes a token field)
- the PostgreSQL DSN hardcodes `sslmode=disable` and the Redis client has no TLS fields: cross-host only inside a
  controlled network until those gaps are closed
- `seedpoc`'s demo password is for the PoC; production passes `-password "$SEED_PASSWORD"` and rotates on first login

## 13. Acceptance checks

| Check | Command | Expected |
|---|---|---|
| containers | `flame ps` | all running / healthy |
| entry point | `curl -sI http://localhost:27001/` | 200 |
| api health | `curl -s http://localhost:27001/health` | 200 once `db` / `redis` / `litellm` / `sandbox` are all up; a 503 body names the missing one |
| registration | `curl -s 'localhost:8500/v1/health/service/sandboxd?passing=1'` | one entry per executor, all passing |
| gateway pool | `flame logs sandboxg \| grep -i backend` | instance count matches |
| browser pool | `flame logs browserg \| grep -i browserd` | instance count matches |
| shared storage | write a file through a sandbox, download it | correct content, no 404 |
| stickiness | two consecutive interactive calls in one session | same backend |
| replica health | `curl -s http://<replica intranet IP>:27006/health` | 200; the body names no failed dependency |
| replica in rotation (§8.2) | log in through the entry point, then reload a few times so requests land on both | still logged in, no silent 401 |
| static assets on each nexus | `flame logs nexus \| grep -i "static file server"` | every host logs where it mounted `/static` |

## 14. Troubleshooting

| Symptom | Likely cause | Check / fix |
|---|---|---|
| instance registered but permanently critical | `service_addr` unreachable from the Consul host | `flame exec consul wget -qO- http://<addr>:27081/healthz`; fix the address or the firewall |
| registration missing entirely | registration disabled, bad `registry.address`, or the advertised address could not be derived | read the executor log: it prints the final ID/address or the startup refusal |
| gateway has no backends | empty catalog, wrong `service_name`, or a `127.0.0.1` seed | `curl -s 'localhost:8500/v1/catalog/service/sandboxd'`; remove loopback seeds |
| nexus answers 503 | PostgreSQL, Redis or LiteLLM unreachable, or the sandbox capability snapshot was never fetched | `curl -s localhost:27001/health` names the failing key (`db` / `redis` / `litellm` / `sandbox`) |
| files appear and disappear | storage is not actually shared | compare `stat` of the same path on two nodes |
| replica healthy but receives nothing | nginx still points at the Compose service name (`http://nexus:27006`) | add the explicit `upstream` from §8.2 |
| a second host runs its own PostgreSQL / Redis | a bare `flame up -d` started the whole stack there | `flame ps` on that host should list `nexus` (or one gateway) only |
| nginx 502 | nexus not up yet | `flame ps`, `flame logs nexus` |
| `session lost` | the instance holding the session restarted or was evicted | expected; a browser session cannot migrate |

## 15. Known gaps

1. `deploy/prod/` has no real artifacts yet: the compose files, units and wrapper above are the specification.
2. Tier 3 has no orchestration artifact — which service starts on which host is still a manual choice; only tiers 1–2 are
   validated.
3. Consul ACLs are not generated by any script; PostgreSQL / Redis have no TLS; cross-machine nexus is unverified.
4. `nexus`'s own registration is off by default and passes `registry.service_addr` through verbatim, unlike the executors
   (which normalise it through `ResolveAdvertise`). The default is `http://localhost`, so enabling it on several hosts as
   it stands collides in Consul and probes `http://http://…`; set a bare per-host IP until the code normalises the value.
5. On tier 1, `127.0.0.1:8500` is a required port (the native executors register there), not an unnecessary exposure.
