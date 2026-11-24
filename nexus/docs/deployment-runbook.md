# Flame deployment runbook

[中文](deployment-runbook.zh.md) | English

> Status: the operational companion to [`deployment.md`](deployment.md). That file explains *why*; this one lists the
> exact steps, files and checks. `deploy/prod/` exists: the compose fragments, units and scripts below are taken from
> those files, and the commands match their interfaces. Fragments omit comments and rarer sections — the files win.
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
│  ├─ bin/{sandboxd,codehost,browserd,browserhost}   # native units only
│  ├─ compose.yaml
│  └─ compose.multi-host.yaml
└─ rootfs/<ver>/          # nsjail rootfs, exported from flame/sandbox:<ver> (§5.3)

/etc/flame/
├─ flame.env             # secrets, addresses, switches (chmod 600)
├─ nexus.yaml
├─ sandboxd.yaml
├─ sandboxd.env          # CONSUL_HTTP_TOKEN, when ACLs are on
├─ browserd.yaml
├─ sandboxg.yaml
├─ browserg.yaml
└─ tls/{fullchain,privkey}.pem   # optional; present enables 27443

/srv/flame/data/{tenants,platform-assets,plugins}
/var/lib/flame/sandboxd/tmp/       # sandboxd tmp_dir, host-local, exclusive
/var/lib/flame/browserd/profiles/  # Chromium profiles, host-local
/var/log/flame/<service>/
```

Containers come from images, natives from the release directory: `flame/nexus:<tag>`, `flame/gateway:<tag>` and
`flame/web:<tag>` are pulled or built once, while `releases/<ver>/bin` holds only the two executors that run as systemd
units. Both sides carry the same `FLAME_TAG`, which is why a release is one number, not two.

`nexus` takes **no `-config` flag**: it reads `./config.yaml` from its working directory, so the container sets
`working_dir: /etc/flame` and mounts `/etc/flame/nexus.yaml` as `/etc/flame/config.yaml`. `server.static_root` stays
`/opt/flame/static` — inside the image, not a bind mount. The other four services accept `-config` and read
`/etc/flame/<name>.yaml` directly.

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
FLAME_TAG=2026.12.01               # the one version number: image tags and releases/<ver>
FLAME_BIND_IP=127.0.0.1            # tiers 2/3: this host's intranet IP
COMPOSE_FILE=compose.yaml          # tier 3: compose.yaml:compose.multi-host.yaml
FLAME_PUBLISH_IP=0.0.0.0           # where 27001 binds; behind an upstream LB: 127.0.0.1 or the intranet IP
FLAME_TLS_IP=0.0.0.0               # where 27443 binds; inert while no certificate exists

# Hex throughout: POSTGRES_PASSWORD is embedded in LiteLLM's DATABASE_URL, where base64's + and / break the URI
POSTGRES_PASSWORD=<openssl rand -hex 24>
REDIS_PASSWORD=<openssl rand -hex 24>
JWT_SECRET=<openssl rand -hex 24>
FLAME_VALUE_KEY=<openssl rand -hex 24>
LITELLM_MASTER_KEY=sk-<openssl rand -hex 24>
LITELLM_SALT_KEY=<openssl rand -hex 24>     # never rotate: it decrypts the provider keys in LiteLLM's database
SANDBOX_TOKEN=<openssl rand -hex 24>
BROWSER_TOKEN=<openssl rand -hex 24>

# Seed credentials, identical to the demo box (password 12345678, fixed API key); change the password after the
# first login and rotate the key, see §12
FLAME_ADMIN_PASSWORD=12345678
FLAME_API_KEY=sk-poc-t0-primary
FLAME_TENANT_SCHEMA=t0
FLAME_TENANT_TITLE=Default tenant

FLAME_DB_HOST=postgres
FLAME_REDIS_ADDR=redis:6379
FLAME_LITELLM_HOST=litellm
# Full URLs: nexus dials these. Empty means "no such executor" (nexus then skips the sandbox readiness gate
# entirely), which is only right for a trial
FLAME_SANDBOX_URL=ws://sandboxg:27080
FLAME_BROWSER_URL=ws://browserg:27090

# Consul, two views: the host-side executors, and the gateway containers (tier 3 with remote gateways: the
# first host's intranet IP)
FLAME_CONSUL_ADDR_HOST=127.0.0.1:8500
FLAME_CONSUL_ADDR_NET=consul:8500
```

### 5.2 `/etc/flame/nexus.yaml` (full `${VAR}` expansion, mounted as `config.yaml`)

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
# /etc/flame/sandboxd.yaml
listen: "0.0.0.0:27081"
token: "${SANDBOX_TOKEN}"
mode: docker
allowed_mount_roots:
  - "/srv/flame/data"
tmp_dir: "/var/lib/flame/sandboxd/tmp"
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
# /etc/flame/browserd.yaml
listen: "0.0.0.0:27091"
token: "${BROWSER_TOKEN}"
workspace_root: "/srv/flame/data"
browser:
  # Nested, not top level: a flat `profile_root` is not a known key and is ignored,
  # and the default (`run/browserd/profiles`, relative to the cwd) fills a disk nobody watches.
  # Per session, and deliberately outside the tenant tree - it holds cookies for that session only.
  profile_root: "/var/lib/flame/browserd/profiles"
registry:
  enabled: true
  type: consul
  address: "<first host intranet IP>:8500"
  service_name: browserd
```

`install.sh` renders them once from `deploy/prod/config/<name>.yaml` into `/etc/flame/<name>.yaml` (`0640
root:flame`), and **only when the file is absent**; `--force-config` overwrites. An upgrade therefore cannot clobber
host policy or credentials.

The rendering is `sed` over a known variable list, not `envsubst`: `envsubst` replaces every `$NAME` it can resolve,
so an nginx-style `$host` / `$http_upgrade` in the same template would be eaten. `install.sh` warns when a `${...}`
survives the render.

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
# /etc/flame/sandboxg.yaml
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
    extra_hosts: ["host.docker.internal:host-gateway"]   # it issues the health checks, so it resolves service_addr

  postgres:
    image: postgres:18
    environment:
      POSTGRES_USER: flame                 # initdb creates the role; no "create then hand over" script
      POSTGRES_PASSWORD: "${POSTGRES_PASSWORD}"
      POSTGRES_DB: flame
    shm_size: 256mb                        # the default 64 MB /dev/shm breaks parallel queries
    volumes:
      # Mount the parent: the 18 image moved PGDATA to /var/lib/postgresql/18/docker, so a volume on the
      # old /var/lib/postgresql/data path would silently stop persisting.
      - flame-pg:/var/lib/postgresql
      - /srv/flame/pg-init:/docker-entrypoint-initdb.d:ro

  redis:
    image: redis:7-alpine
    command: ["redis-server", "/etc/flame/redis.conf"]   # the password lives in the file, not in argv
    volumes:
      - flame-redis:/data
      - /etc/flame/redis.conf:/etc/flame/redis.conf:ro

  litellm:
    image: ghcr.io/berriai/litellm-database:main-latest
    environment:
      DATABASE_URL: "postgresql://flame:${POSTGRES_PASSWORD}@postgres:5432/litellm?sslmode=disable"
      LITELLM_MASTER_KEY: "${LITELLM_MASTER_KEY}"
      LITELLM_SALT_KEY: "${LITELLM_SALT_KEY}"          # decrypts the stored provider keys; never rotate

  nexus:
    image: flame/nexus:${FLAME_TAG}
    user: "1000:1000"
    working_dir: /var/lib/flame/nexus      # nexus reads ./config.yaml here and creates logs/ next to it
    env_file: ["/etc/flame/flame.env"]     # nexus expands the ${VAR} placeholders from this environment
    environment: { XLOG_CONSOLE: "1" }     # otherwise logs only reach a file and `docker logs` is empty
    volumes:
      - /etc/flame/nexus.yaml:/var/lib/flame/nexus/config.yaml:ro
      - /var/log/flame/nexus:/var/lib/flame/nexus/logs
      - /srv/flame/data:/srv/flame/data

  sandboxg:
    image: flame/gateway:${FLAME_TAG}
    user: "1000:1000"
    command: ["/opt/flame/bin/sandboxg", "-config", "/etc/flame/config.yaml"]
    environment: { XLOG_CONSOLE: "1" }
    volumes:
      - /etc/flame/sandboxg.yaml:/etc/flame/config.yaml:ro
      - /var/log/flame/sandboxg:/var/log/flame/sandboxg
    extra_hosts: ["host.docker.internal:host-gateway"]

  browserg:
    image: flame/gateway:${FLAME_TAG}
    user: "1000:1000"
    command: ["/opt/flame/bin/browserg", "-config", "/etc/flame/config.yaml"]
    environment: { XLOG_CONSOLE: "1" }
    volumes:
      - /etc/flame/browserg.yaml:/etc/flame/config.yaml:ro
      - /var/log/flame/browserg:/var/log/flame/browserg
    extra_hosts: ["host.docker.internal:host-gateway"]

  web:
    image: flame/web:${FLAME_TAG}
    # Container port equals host port (nginx listens on 27001 / 27443); the variable decides the binding address
    ports:
      - "${FLAME_PUBLISH_IP:-0.0.0.0}:27001:27001"
      - "${FLAME_TLS_IP:-0.0.0.0}:27443:27443"   # nginx only listens there with a certificate; publishing it is harmless
    volumes:
      - /srv/flame/data:/srv/flame/data:ro   # X-Accel-Redirect: nginx sends the bytes itself
      - /etc/flame/tls:/etc/nginx/tls:ro     # optional; the start hook links the TLS block in when it exists
```

Two one-shot jobs also live in this file and are never started by `up -d` — they sit behind the `jobs` profile, so they
are invoked as `flame --profile jobs run --rm <name>`: `migrate` — the `postgres` image running psql over
`releases/<ver>/sql`, in the order `scripts/migrate-apply.sh` lists — and `bootstrap` — the `flame/nexus` image with an
overridden entrypoint running the seed script. Both need nexus's `user: "1000:1000"` and the `/srv/flame/data` mount,
or the files they write end up owned by a uid neither nexus nor the sandbox can read.

Images, not a release directory: nothing in the application plane is bind-mounted from `/opt/flame` any more. A new
`FLAME_TAG` changes the image each container references, which is what makes Compose recreate it — no `--force-recreate`
needed, and `releases/` matters only to the two systemd units.

`/srv/flame/pg-init/10-litellm-db.sql` runs once, on an empty data volume. The role and the `flame` database come from
`POSTGRES_USER` / `POSTGRES_DB` (with `POSTGRES_PASSWORD`), so the file only owns the second database — in an idempotent
form, so running it by hand on a database that already has it does not fail:

```sql
SELECT 'CREATE DATABASE litellm OWNER flame'
WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = 'litellm')\gexec
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

### 7.0 See it work on your own machine first (no root)

```bash
./scripts/images.sh build --tag dev --no-sandbox
./scripts/dev-up.sh            # data plane -> SQL -> seed -> local sandboxd -> application plane
```

It uses `compose.dev.yaml`: configuration is rendered into `.devrun/`, data lives in named volumes, and it installs
**no systemd units and writes nothing into `/etc/flame`**. It finishes by printing the console URL and two accounts
(platform `plat`, tenant `primary`, password `12345678`). The seed gives the tenant a `DeepSeek` provider with an
empty key: paste yours in the console and provision its `deepseek-flash` model. `dev-up.sh --smoke` additionally runs
the end-to-end call (it needs `FLAME_LLM_BASE_URL` / `FLAME_LLM_API_KEY` / `FLAME_LLM_MODEL`), and `dev-up.sh
down|purge|status|logs` are the companions.

Boundaries of the trial: the seeded provider has no API key (so nothing answers until you add one), no `browserd`,
and `sandboxd` runs as a process on this machine, so shell tools execute but see none of the tenant's files. A real
installation is 7.1.

### 7.1 The production host

```bash
# 0) on the build machine (go, node and docker): products, images, both tarballs and SHA256SUMS in one directory.
#    Copy the two archives over, then extract the tree where they sit (here /srv/flame/dist) - install.sh reads both
#    from that one directory, so nothing has to be sorted out on the host.
./scripts/release.sh --tag "$FLAME_TAG"

# 1) preflight: either it passes or it names what is missing (it changes nothing)
sudo ./scripts/install.sh --tag "$FLAME_TAG" --check

# 2) place: release, units, wrapper, the four rendered configs, nexus.yaml and redis.conf; a first run also writes
#    /etc/flame/flame.env. --images points at the directory holding the image tarball: it docker-loads first.
sudo ./scripts/install.sh --tag "$FLAME_TAG" --images /srv/flame/dist
#    ... or, with the images published to a registry (`./scripts/images.sh push --tag "$FLAME_TAG" --registry <ns>`):
#    it pulls the four runtime images and records <ns> as FLAME_IMAGE_NS in flame.env.
# sudo ./scripts/install.sh --tag "$FLAME_TAG" --registry ghcr.io/acme/flame

# 3) data plane first
flame up -d consul postgres redis litellm

# 4) platform SQL, then the t0 seed — both are one-shot compose services, both idempotent
./scripts/migrate.sh --check                # the file set must equal the execution list
./scripts/migrate.sh
flame --profile jobs run --rm bootstrap     # writes /etc/flame/initial-credentials.txt, 0600, owned by flame

# 5) native executors, then the application plane (install.sh already enabled them; this only starts)
systemctl start flame-sandboxd flame-browserd
flame up -d
flame ps

# 6) acceptance: the §13 checks, naming whatever fails
./scripts/health.sh
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
EnvironmentFile=-/etc/flame/sandboxd.env       # CONSUL_HTTP_TOKEN, when ACLs are on
ExecStart=/opt/flame/current/bin/sandboxd -config /etc/flame/sandboxd.yaml
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
# docker, the shared mount, /opt/flame/current (bin only), the flame user
install -d -o flame -g flame /var/lib/flame/sandboxd/tmp
# copy /etc/flame/sandboxd.yaml from the first host, then edit registry.address to its intranet IP
# service_addr stays empty: it is derived from listen, and the ID follows the address
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

New host — no release directory at all, just an env file, a config and the image:

```bash
# 1) the flame wrapper, /opt/flame/current (compose files only), /etc/flame/flame.env with this host's
#    FLAME_BIND_IP, the same COMPOSE_FILE, the same FLAME_TAG and the same secrets
# 2) overlay: publish this host's own 27006 and nothing else - that is what nginx dials
#    nexus: { ports: ["${FLAME_BIND_IP:?set FLAME_BIND_IP}:27006:27006"] }
# 3) start one service, not the stack: a bare `flame up -d` would also create
#    PostgreSQL / Redis / LiteLLM / Consul here, and those belong to the first host
flame up -d nexus
```

Config: the first host's `nexus.yaml` with the addresses rewritten —

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

Do **not** run `migrate` or `bootstrap` here: both are one-shot jobs that belong to the first host (§7), and two hosts
applying DDL at once is exactly the lock wait the split exists to avoid. PostgreSQL / Redis / LiteLLM do not belong on this
host either.

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
# upgrade: build, then move one version number — containers follow the image tag, natives follow the symlink
./scripts/build.sh
./scripts/images.sh build --tag "$FLAME_TAG"
sudo ./scripts/install.sh --tag "$FLAME_TAG"        # places releases/<ver>, repoints current, reloads the units
./scripts/migrate.sh                                # SQL before the new code runs
flame up -d sandboxg browserg nexus web             # the image reference changed, so Compose recreates each
systemctl restart flame-sandboxd flame-browserd     # re-exec through the repointed symlink

# revert to the previous release (not the version-domain "rollback")
sudo ./scripts/install.sh --tag "<old>"
flame up -d sandboxg browserg nexus web
systemctl restart flame-sandboxd flame-browserd
```

No `--force-recreate` is needed on the container side: the service definition changes (`image: flame/nexus:<new>`), which
is exactly the per-service config hash Compose compares, and nothing in the application plane is bind-mounted from
`/opt/flame` any more. The two natives are the opposite case — their unit path is unchanged, so only `install.sh` repointing
`current` plus a `restart` makes them run the new binary.

`bootstrap` is not part of an ordinary upgrade: it exists to create t0 on an empty database and is idempotent, but running
it after every release only re-prints credentials nobody asked for.

`mode: nsjail` adds one step: a rootfs belongs to an image generation (§5.3), so build the new one, point `rootfs:` at
it, then restart `flame-sandboxd`. Skipping it leaves the jail on the previous interpreters while docker mode — if that is
mixed in — runs the new image. Keep the old rootfs until the new one has taken traffic; deleting it makes the revert
impossible without rebuilding.

Constraint: SQL is additive only (new tables, new columns). A destructive change makes the old code run on a new schema,
and a revert stops being safe. Take a database dump before every migration.

With a replica (§8.2) `migrate` runs once, on the first host, but every host repeats `install.sh --tag` and
`flame up -d nexus`: each has its own `FLAME_TAG`, its own `current` symlink and its own local images. Upgrade one host at a
time and keep nginx pointed at the host already upgraded, not at one still on the old tag.

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
- `bootstrap`'s seed credentials (`12345678`, the fixed key `sk-poc-t0-primary`) match the PoC and `install.sh` writes
  exactly those into `flame.env`: change the password after the first login, rotate the key, then delete
  `initial-credentials.txt`

## 13. Acceptance checks

| Check | Command | Expected |
|---|---|---|
| everything above at once | `./scripts/health.sh` | all ok; a FAIL names the item |
| it can actually answer | `./scripts/smoke.sh` (needs `FLAME_LLM_*` for a real provider) | the answer contains the random string the script sent - the only check that proves "usable" |
| containers | `flame ps` | all running / healthy |
| images for this tag | `docker image inspect flame/nexus:$FLAME_TAG flame/gateway:$FLAME_TAG flame/web:$FLAME_TAG flame/sandbox:$FLAME_TAG` | all four present |
| platform SQL | `./scripts/migrate.sh --check` | the file set equals the execution list |
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

1. `bootstrap` is still compiled from `nexus/cmd/seedpoc` and installed under the production name: the behaviour matches
   the contract in §7, and the rename is what is left.
2. The platform SQL has no version table (every script is re-applied in a fixed order and relies on idempotence), and
   tenant schemas have no upgrade mechanism at all; see `deployment.md` §11 items 7 and 8.
3. Tier 3 has artifacts but no end-to-end validation — which service starts on which host is still a manual choice;
   only tiers 1–2 are validated.
4. Consul ACLs are not generated by any script; PostgreSQL / Redis have no TLS; cross-machine nexus is unverified.
5. `nexus`'s own registration is off by default and passes `registry.service_addr` through verbatim, unlike the executors
   (which normalise it through `ResolveAdvertise`). The default is `http://localhost`, so enabling it on several hosts as
   it stands collides in Consul and probes `http://http://…`; set a bare per-host IP until the code normalises the value.
6. On tier 1, `127.0.0.1:8500` is a required port (the native executors register there), not an unnecessary exposure.
