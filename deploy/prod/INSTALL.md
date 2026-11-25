# Installing Flame in production

English | [中文](INSTALL.zh.md)

The step-by-step, for a host with no toolchain. The reasoning lives in
[`nexus/docs/deployment.md`](../../nexus/docs/deployment.md); the acceptance checks are in
[`nexus/docs/deployment-runbook.md`](../../nexus/docs/deployment-runbook.md). This file is the part you follow
command by command: **one host first (tier 1), then the same install grows to several hosts without being redone.**

`<owner>` below is the GitHub account that publishes the images; in this repository it is `buffalo-xi`.

## 0. What the host needs

| | |
|---|---|
| Architecture | `linux/amd64` (the published images are amd64 only) |
| Access | root, systemd |
| Runtime | Docker with the `compose` plugin |
| Network | `github.com` and `ghcr.io` reachable; or use the offline bundle (§1b) |
| Ports | 27001 free (27443 is published but only listens with a certificate); the executors listen on `0.0.0.0:27081` / `0.0.0.0:27091`, so keep them off the public network |
| Storage | `/srv/flame/data` on the local filesystem, owned by uid 1000 (a shared mount from tier 2 on) |
| Kernel | docker mode: the sandbox runs containers, so the daemon must allow nested Docker; nsjail mode: cgroup v2 instead (§4) |

## 1. Get the artifacts

Two paths; pick one. Both end in the same `/srv/flame/dist`, because everything below runs from there.

### 1a. From a release (needs `ghcr.io`)

```bash
mkdir -p /srv/flame/dist && cd /srv/flame/dist
curl -fLO https://github.com/<owner>/flame/releases/download/v2026.11.25/flame-prod-v2026.11.25.tar.gz
curl -fLO https://github.com/<owner>/flame/releases/download/v2026.11.25/SHA256SUMS
sha256sum -c SHA256SUMS
tar -xzf flame-prod-v2026.11.25.tar.gz
```

The tarball is the **host half** of a deployment: the two executor binaries that cannot be containers
(`sandboxd`, `browserd`) plus `browserhost`/`codehost`, the systemd units, the configuration templates and these
scripts. The four runtime images (nexus, gateway, web, sandbox) come from GHCR; `base` is build-time only.

### 1b. Offline (needs neither registry nor internet on the target)

On a build machine with Go, Node and Docker:

```bash
cd deploy/prod
./scripts/release.sh --tag 2026.11.25        # products + images + both tarballs + SHA256SUMS
```

Copy `release/` to the host, extract the tree there, and pass `--images` instead of `--registry` in step 2.2. The
bundle then carries the four upstream images too (consul, postgres, redis, litellm).

## 2. Single host (tier 1)

Everything — data plane, application plane, both executors — on one machine.

### 2.1 Preflight (changes nothing)

```bash
sudo ./scripts/install.sh --tag v2026.11.25 --check --registry ghcr.io/<owner>/flame
```

It checks Docker, the compose plugin, openssl, uid 1000 availability, the storage mount and port conflicts, and
resolves each image's manifest against the registry without pulling it. Fix every `FAIL` before continuing; `warn`
lines are informational (no Chromium on PATH, ports already in use from a previous install, …).

### 2.2 Place the release

```bash
sudo ./scripts/install.sh --tag v2026.11.25 --registry ghcr.io/<owner>/flame
# offline bundle instead:
# sudo ./scripts/install.sh --tag v2026.11.25 --images /srv/flame/dist
```

This pulls the four runtime images, places `/opt/flame/releases/<tag>` and points `/opt/flame/current` at it, writes
`/etc/flame/flame.env` with fresh secrets (0600), renders the four service configurations plus `redis.conf`, installs
`/usr/local/bin/flame` and the units, and **starts nothing**.

Then verify the fifth configuration is in place — the installer writes it from every release that includes this
file, and the check is a no-op when it is already there:

```bash
sudo test -f /etc/flame/nexus.yaml || \
  sudo install -m 0640 -o root -g flame config/nexus.yaml /etc/flame/nexus.yaml
```

`nexus.yaml` is the one configuration that is **copied verbatim** rather than rendered, because nexus expands its
`${VAR}` placeholders from its own environment.

### 2.3 Start, in order

The order is the installation: data plane, then schema, then seed, then executors, then the application plane.

```bash
flame up -d consul postgres redis litellm     # data plane
./scripts/migrate.sh --check                  # the SQL file set must equal the execution list
./scripts/migrate.sh
flame --profile jobs run --rm bootstrap       # writes /etc/flame/initial-credentials.txt (0600)
systemctl start flame-sandboxd flame-browserd # the two bare-metal executors
flame up -d                                   # nexus, both gateways, web
flame ps
./scripts/health.sh                           # acceptance: names whatever fails
```

`install.sh` already enabled the units; `systemctl start` is the only thing it left to you, and only after the
schema and the seed exist.

### 2.4 First login

Open `http://<host>:27001`.

| | |
|---|---|
| platform administrator | `plat` / `12345678` |
| tenant administrator (`t0`) | `primary` / `12345678` |

Those are the seeded PoC credentials. **Change the password and rotate `FLAME_API_KEY` immediately** — both live in
`/etc/flame/flame.env`. Each provider's API key is added in the console, and every model call goes through LiteLLM.

## 3. Growing to several hosts

No reinstall anywhere: the same release, the same images, and a few lines in `/etc/flame/flame.env`. Adding the
first executor node also requires the shared storage mount, because a sandbox writes files the application plane
serves.

| | tier 1 (this file) | tier 2 (+ executors) | tier 3 (containers split) |
|---|---|---|---|
| data plane, nexus, gateways | one host | same | spread across hosts |
| `sandboxd` / `browserd` | same host | extra hosts, bare | same as tier 2 |
| `FLAME_BIND_IP` | `127.0.0.1` | this host's intranet IP | same |
| `FLAME_CONSUL_ADDR_HOST` | `127.0.0.1:8500` | `<first host>:8500` | same |
| `COMPOSE_FILE` | `compose.yaml` | unchanged | `compose.yaml:compose.multi-host.yaml` |
| `/srv/flame/data` | local directory | **shared mount, same path, uid 1000** | same |

### 3.1 Add an executor node (tier 1 → 2)

On the first host, stop keeping Consul on loopback:

```bash
# /etc/flame/flame.env: FLAME_BIND_IP=<this host's intranet IP> and FLAME_CONSUL_ADDR_HOST=<this host's intranet IP>:8500
flame up -d consul                                   # re-create so the new binding applies; the volume keeps the catalog
curl -s "http://<first host>:8500/v1/status/leader"  # prove it is reachable before touching the new node
```

The gateways need no change: discovery pulls from Consul, so a new executor joins on the next pull (≤ 5 s).

On the new host:

```bash
# docker, the same /srv/flame/data mount, the flame user (uid 1000), and /opt/flame/current/bin (binaries only —
# the containers, the compose files and the SQL stay on the first host)
install -d -o flame -g flame /var/lib/flame/sandboxd/tmp
# copy /etc/flame/sandboxd.yaml from the first host and set registry.address to <first host>:8500;
# leave registry.service_addr empty — it is derived from listen, and the ID follows the address
systemctl enable --now flame-sandboxd
```

`sandboxd` runs `flame/sandbox:<tag>` containers on this node but never pulls them itself: give the node access to
GHCR (the reference comes from `sandboxd.yaml`, as `FLAME_IMAGE_NS` plus `FLAME_TAG`), or load the bundle with
`./scripts/images.sh load --dir /srv/flame/dist`.

Firewall: new node → first host `8500` (registration); first host and every gateway node → new node `27081`
(health check and traffic).

Verify the catalog *grew* rather than replaced the existing entry:

```bash
curl -s "http://<first host>:8500/v1/health/service/sandboxd?passing=1"
```

Rollback: `systemctl disable --now flame-sandboxd` on the new node.

### 3.2 Split the containers (tier 2 → 3)

Precondition: `/srv/flame/data` is already the same directory everywhere. Without it a replica serves half the
files and nobody notices until a download 404s.

Tier 3 is not a rewrite either — the first host publishes what the other hosts need, and each new host gets an env
file, a configuration and the image (no release directory):

```bash
# first host — /etc/flame/flame.env: COMPOSE_FILE=compose.yaml:compose.multi-host.yaml
flame up -d
```

The overlay adds only `ports`, and only for what is actually split: PostgreSQL `5432`, Redis `6379` and LiteLLM
`4000` are live because nexus usually is not on the database machine; `nexus` `27006`, `sandboxg` `27080` and
`browserg` `27090` are commented out — uncomment the one block for the machine you really separated, since publishing
a port is a firewall decision, not a default.

On the new nexus host, `config/nexus.yaml` points `db.host`, `redis.addr`, `litellm.host`, `sandbox.url` and
`browser.url` at the first host's intranet address. Three values must match everywhere: `JWT_SECRET`,
`FLAME_VALUE_KEY`, `LITELLM_SALT_KEY`.

Known boundary: tier 3 has artifacts but has **not** been verified end to end; tiers 1 and 2 are the validated
paths. Tier 3 also has no orchestration artifact — which service each machine starts is still a manual choice.

## 4. Optional: the nsjail isolation mode

Docker mode runs every sandbox as a container on the host daemon. nsjail runs it as a jailed process instead: no daemon,
no image needed at runtime, and no nested-Docker question — but it charges a different price, a **setuid** binary, cgroup
v2, and a rootfs you export from the same sandbox image. The mode lives in one host's `/etc/flame/sandboxd.yaml`, so the
two can be mixed in one pool; pick per host, not per deployment.

### 4.1 What an nsjail node needs

| | |
|---|---|
| `nsjail` | built from source (Ubuntu 26.04 packages none) and installed **setuid root** — it must be able to change namespaces while `sandboxd` keeps running as `flame` |
| cgroup v2 | `stat -fc %T /sys/fs/cgroup` prints `cgroup2fs`; memory limits go through `--cgroup_mem_max` |
| rootfs | `/opt/flame/rootfs/<tag>`, exported from `flame/sandbox:<tag>` — one per image generation, ~600 MB |
| `codehost` | inside that rootfs at `/usr/local/bin/codehost`, taken from the same image, never copied by hand |
| `/dev` | `null`, `zero`, `random`, `urandom`, `tty` inside the rootfs — `docker export` carries none (`codehost` opens `/dev/null` for every child) |

```bash
sudo apt install -y bison flex libprotobuf-dev protobuf-compiler libnl-3-dev libnl-route-3-dev libcap-dev pkg-config
git clone https://github.com/google/nsjail && cd nsjail && make
sudo install -m 4755 -o root -g root nsjail /usr/local/bin/nsjail   # 4755 = setuid root: without it nsjail cannot create namespaces for a non-root caller
stat -fc %T /sys/fs/cgroup                                          # cgroup2fs, or this mode is not for this host
```

`install.sh --check` only **warns** about a missing `nsjail` (docker mode is the default), so that line is worth reading
rather than skimming.

### 4.2 On the single host

The tag and the image namespace live in `/etc/flame/flame.env`, and the rootfs path has to name the tag — one rootfs per
image generation, or the interpreters nsjail runs drift away from the ones the image actually has:

```bash
FLAME_TAG=$(sed -n 's/^FLAME_TAG=//p' /etc/flame/flame.env)
NS=$(sed -n 's/^FLAME_IMAGE_NS=//p' /etc/flame/flame.env)
R=/opt/flame/rootfs/$FLAME_TAG

# export, do not hand-build: the image is the contract (interpreters, packages, codehost)
sudo docker create --name flame-rootfs "$NS/sandbox:$FLAME_TAG" true
sudo install -d "$R"
sudo docker export flame-rootfs | sudo tar -x -C "$R"
sudo docker rm flame-rootfs

# the device nodes docker export does not carry
sudo install -d "$R/dev"
sudo mknod -m 666 "$R/dev/null"    c 1 3
sudo mknod -m 666 "$R/dev/zero"    c 1 5
sudo mknod -m 666 "$R/dev/random"  c 1 8
sudo mknod -m 666 "$R/dev/urandom" c 1 9
sudo mknod -m 666 "$R/dev/tty"     c 5 0

# prove it before sandboxd trusts it: a broken interpreter only shows up per request, as "unavailable"
sudo nsjail -Mo --chroot "$R" -- /usr/bin/python3 -V
sudo nsjail -Mo --chroot "$R" -- /usr/local/bin/lua -v
```

Then the host policy file — the installer rendered it once and will not touch it again:

```yaml
# /etc/flame/sandboxd.yaml
mode: nsjail

nsjail:
  binary: /usr/local/bin/nsjail     # absolute: the unit's PATH is systemd's, not yours
  rootfs: /opt/flame/rootfs/v2026.11.25
  uid: 99999
  gid: 99999
  disable_network: true
  read_only_rootfs: true            # the tree is mounted read-only; nothing is repaired at runtime
  cgroup_version: auto
  # extra_args: ["-R", "/dev"]      # only when mknod is impossible — see 4.3
```

```bash
sudo systemctl restart flame-sandboxd && ./scripts/health.sh
```

Three things that are easy to get wrong here:

- **`--force-config` puts `mode: docker` back.** It re-renders `/etc/flame/sandboxd.yaml` from the release's template,
  so after using it, re-apply the block above.
- **`docker.image` is ignored in this mode.** The rootfs plays its part, so the node needs no sandbox image at runtime —
  but it needs one **once** to export, and that is why the path carries the tag.
- **`network: on-request` cannot grant egress by itself.** Under nsjail, cutting the network is `clone_newnet` plus
  `--iface_no_lo`; an egress request needs `--disable_clone_newnet` in `extra_args`. `sandboxd` logs this and starts
  anyway, so "egress requests silently get no network" is the failure you will see, not a refusal to boot.

### 4.3 When `mknod` is not possible

No root on a dev machine, or Docker Desktop's overlay refusing the call, leaves the rootfs without device nodes. Put
`extra_args: ["-R", "/dev"]` in the block instead: it bind-mounts the host `/dev` read-only into the jail, which supplies
`null` / `zero` / `random` / `urandom` / `tty` without root. On a normal root host, prefer `mknod` — the bind hands the
jail more devices than it asks for.

### 4.4 Multi-host (tier 2 and tier 3)

The mode is per host and nothing else records it: Consul only carries an address, and the gateway does not know or care
which isolation a node uses. So a pool can mix docker and nsjail nodes — but then the **image generation has to match**,
or the same request answers with different interpreters depending on where it lands.

Every nsjail executor node needs its own copy of all three: the setuid binary, the rootfs export of the **same tag**, and
the device nodes. The release tarball carries neither `nsjail` nor a rootfs (§1), and `install.sh` creates
`/opt/flame/rootfs` empty — this is the step it leaves to you.

So §3.1 gains one step on the new node, before `systemctl enable --now flame-sandboxd`:

```bash
FLAME_TAG=$(sed -n 's/^FLAME_TAG=//p' /etc/flame/flame.env)   # copied from the first host, or written by install.sh
NS=$(sed -n 's/^FLAME_IMAGE_NS=//p' /etc/flame/flame.env)
R=/opt/flame/rootfs/$FLAME_TAG
sudo docker create --name flame-rootfs "$NS/sandbox:$FLAME_TAG" true   # the image must exist on this node once
sudo install -d "$R" && sudo docker export flame-rootfs | sudo tar -x -C "$R" && sudo docker rm flame-rootfs
sudo install -d "$R/dev" && sudo mknod -m 666 "$R/dev/null" c 1 3   # …and zero / random / urandom / tty
sudo nsjail -Mo --chroot "$R" -- /usr/bin/python3 -V
```

Everything else is unchanged: the copied `/etc/flame/sandboxd.yaml` already names the tag, registration is the same
`8500`, and the firewall is the same `27081`. Tier 3 changes nothing about this mode — `sandboxd` is bare on both tiers
because it is the component that holds the privilege; what tier 3 spreads out is nexus, the gateways and the data plane.

Keep `/opt/flame/rootfs` **machine-local**, one directory per generation. A shared mount of it is not forbidden — the tag
directory is the same generation either way — but every exec would then read its interpreters over the network, and
`/srv/flame/data` stays the one mount that has to be shared.

### 4.5 Upgrading with nsjail

`FLAME_TAG` is still the version, but the rootfs does **not** follow it: `install.sh --tag …` does not touch
`/etc/flame/sandboxd.yaml`, so a node keeps running the previous generation's interpreters with nothing reporting the
difference. The order is export first, repoint second:

```bash
# 1. the new generation, in its own directory (the old one stays until the new one has taken traffic)
FLAME_TAG=v2026.11.26; NS=$(sed -n 's/^FLAME_IMAGE_NS=//p' /etc/flame/flame.env); R=/opt/flame/rootfs/$FLAME_TAG
docker pull "$NS/sandbox:$FLAME_TAG"          # or ./scripts/images.sh load --dir <bundle>
docker create --name flame-rootfs "$NS/sandbox:$FLAME_TAG" true
sudo install -d "$R" && docker export flame-rootfs | sudo tar -x -C "$R" && docker rm flame-rootfs
sudo install -d "$R/dev" && sudo mknod …      # the same five nodes as 4.2
sudo nsjail -Mo --chroot "$R" -- /usr/bin/python3 -V

# 2. repoint, then the normal upgrade from §5
sudo sed -i "s#^  rootfs: .*#  rootfs: $R#" /etc/flame/sandboxd.yaml
sudo ./scripts/install.sh --tag v2026.11.26 --registry ghcr.io/<owner>/flame
sudo sed -i 's/^FLAME_TAG=.*/FLAME_TAG=v2026.11.26/' /etc/flame/flame.env
flame up -d && sudo systemctl restart flame-sandboxd
```

Rolling back is pointing `rootfs:` at the previous directory and restarting — which is why the old one is kept until the
new generation has taken traffic. Deleting it makes the rollback a rebuild.

### 4.6 When an nsjail node misbehaves

| Symptom | Cause |
|---|---|
| `isolate: nsjail binary not found ("nsjail")` at startup | not on the unit's `PATH`; name the absolute path in `nsjail.binary` |
| `isolate: nsjail requires rootfs` | `nsjail.rootfs` is empty — the directory is never checked for existence, so a wrong path fails later instead |
| `codehost: start python3: open /dev/null: no such file or directory` | the device nodes are missing — 4.2, or `-R /dev` — 4.3 |
| the language is reported unavailable, nsjail exits `255` | the interpreter path inside the rootfs is wrong, or `/usr/local/bin/lua` exists without its `/usr/bin/lua5.4` target |
| the node registers, then every exec fails with `EPERM` / `ENOSYS` | cgroup v1, or a kernel without user namespaces: check `stat -fc %T /sys/fs/cgroup` and `nsjail --version` first |
| egress requests get no network while `network: on-request` | `--disable_clone_newnet` is missing from `extra_args` (4.2) |

## 5. Upgrade and rollback

`FLAME_TAG` **is** the version: it selects the image tag, the release directory and the row in `flame.env`.

```bash
# upgrade: place the new tag, then move that one line
sudo ./scripts/install.sh --tag v2026.11.26 --registry ghcr.io/<owner>/flame
sudo sed -i 's/^FLAME_TAG=.*/FLAME_TAG=v2026.11.26/' /etc/flame/flame.env
flame up -d && systemctl restart flame-sandboxd flame-browserd

# rollback: put the old tag back and repeat the same two commands
```

`install.sh` never overwrites `/etc/flame/flame.env`, the service configurations or `/srv/flame/data` — they carry
host policy, secrets and tenant data. `--force-config` re-renders the configurations from the new release's
templates (nothing else).

With `mode: nsjail` the upgrade has one step more, and it is the one nothing enforces: the rootfs is not part of the
release, so export the new generation and repoint `rootfs:` before the restart (§4.5).

## 6. When it does not work

| Symptom | Cause |
|---|---|
| nexus restarts in a loop, log says the config cannot be read | `/etc/flame/nexus.yaml` missing — the bind mount creates a directory where its source is absent (step 2.2) |
| `pull access denied` / `unauthorized` | pulling something other than `ghcr.io/<owner>/flame/*`, or the network blocks GHCR |
| a container starts and exits immediately | an upstream image (consul / postgres / redis / litellm) is missing; `install.sh` names it at the end |
| `permission denied` writing tenant files | `/srv/flame/data` is not uid 1000, or two hosts point at different paths |
| `health.sh` reports the sandbox unavailable | `flame-sandboxd` not started, or the gateway cannot reach `27081` |
| `exec format error` | the host is arm64 and the image is amd64; re-run `install.sh --check` on the target architecture |
| an nsjail node registers but cannot run anything | §4.6 — the usual three are a binary not on `PATH`, a rootfs without `/dev` nodes, a stale generation |

## 7. What this file does not cover

- **Model configuration.** The seed gives tenant `t0` a `DeepSeek` provider with an empty key; nothing answers until
  a real key is pasted in the console and its model is provisioned through LiteLLM.
- **Tenant-schema upgrades.** Platform SQL is idempotent and applied in a fixed order; tenant schemas are not
  migrated at all (`deployment.md` §11).
- **TLS certificates.** Dropped into `/etc/flame/tls/`; nginx links the TLS server block only when both
  `fullchain.pem` and `privkey.pem` are present.
- **Backups.** `/srv/flame/data` plus the PostgreSQL volume, and `/etc/flame/flame.env` (without it the encrypted
  values are unreadable).
