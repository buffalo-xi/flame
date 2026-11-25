# Flame production artifacts

English | [中文](README.zh.md)

This directory is the deployable form of [`nexus/docs/deployment.md`](../../nexus/docs/deployment.md) (the
design, and the "why") and [`nexus/docs/deployment-runbook.md`](../../nexus/docs/deployment-runbook.md) (the
step-by-step commands and checks). Read those two for reasoning and acceptance; this file says what is in here and in
which order to use it, and [`INSTALL.md`](INSTALL.md) is that order as commands you can paste — one host first, then
the same install grown onto several.

Deliberately **no Kubernetes, no Ansible, no operator**: one normal Linux host runs the whole product, one
compose file is the launcher, systemd gives it boot ordering and restart, and the two things that hold a host
privilege (`sandboxd`, `browserd`) run bare.

## Install from a GitHub release (nothing to compile)

A `v*` tag publishes five images to GHCR and one tarball to that release. The tarball is the **host half** of a
deployment — the two executor binaries that cannot be containers, the systemd units, the config templates and these
scripts — and the images carry the rest:

```bash
# on the target host, as root; <tag> is the release tag (it is FLAME_TAG, the image tag and the release name at once)
mkdir -p /srv/flame/dist && cd /srv/flame/dist
curl -fLO https://github.com/<owner>/flame/releases/download/<tag>/flame-prod-<tag>.tar.gz
tar -xzf flame-prod-<tag>.tar.gz
sudo ./scripts/install.sh --tag <tag> --check --registry ghcr.io/<owner>/flame    # changes nothing
sudo ./scripts/install.sh --tag <tag> --registry ghcr.io/<owner>/flame
```

Then the start order is the one under "Order of operations" below (data plane → SQL → seed → executors → application
plane). Upgrading is a new tag plus `flame up -d`; rolling back is editing `FLAME_TAG` back in
`/etc/flame/flame.env` — that line is the version.

Two things a host needs from the outside: the four upstream images (consul, postgres, redis, litellm) come from their
own publishers — a host without internet takes the offline tarball instead — and GHCR packages are **private by
default even on a public repository**, so the publisher has to set the package visibility to public (or hand out a
`read:packages` token) once, after the first run.

One dependency this tarball does not carry: `browserd` drives a Chromium or Google Chrome that must already be on the
host `PATH` ([`INSTALL.md`](INSTALL.md) §0). Without one the service still starts, registers and answers `/healthz` —
only the browser tools stay unavailable, and `scripts/health.sh` reports it on its `browser` line.

## Try it on one machine (about a minute)

No root, no systemd, nothing installed outside this directory:

```bash
./scripts/images.sh build --tag dev --no-sandbox   # once: base / nexus / gateway / web
./scripts/dev-up.sh                                # data plane, SQL, seed, executor, web
```

`dev-up.sh` does not stop at "containers are up": it waits until `/health` reports `ok`, so the console can be used
the moment it prints the credentials. The seed gives the tenant a `DeepSeek` provider with an empty API key — paste
yours under Model providers and provision its `deepseek-flash` model. Then open <http://127.0.0.1:27001>:

| | |
|---|---|
| platform administrator | `plat` / `12345678` |
| tenant administrator (tenant `t0`) | `primary` / `12345678` |

`./scripts/dev-up.sh down` stops it (data kept), `purge` deletes the data too.

The trial is honest about two boundaries: the seeded `DeepSeek` provider has no API key (and there is no local echo
model), so nothing answers until you paste a real key and provision the model; and `sandboxd` runs as a plain
process on this machine (it has to — it is the thing that starts containers), so shell tools work but see none of
the tenant's files, while `browserd` is not started and its tools report unavailable. Everything else — console,
login, tenants, agents, endpoints, API keys, model provisioning through LiteLLM — is the real thing.

`./scripts/dev-up.sh --smoke` additionally exercises that path end to end. It needs `FLAME_LLM_BASE_URL`,
`FLAME_LLM_API_KEY` and `FLAME_LLM_MODEL`, and it creates a provider `smoke` plus a model under fixed names, so only
use it when you do not mind those rows appearing in the console.

## What is in here

```
deploy/prod/
├─ compose.yaml                     the stack: consul, postgres, redis, litellm, nexus, two gateways, web
├─ compose.multi-host.yaml          tier 3 overlay: ports only, loaded via COMPOSE_FILE
├─ compose.dev.yaml                 local-trial overlay: .devrun paths, named volumes; never deployed
├─ config/
│  ├─ nexus.yaml                    ${VAR} placeholders, expanded by nexus from its own environment
│  ├─ litellm.yaml                  store_model_in_db + master key: without it provisioning silently no-ops
│  ├─ sandboxd.yaml                 rendered once into /etc/flame (host policy, never overwritten)
│  ├─ browserd.yaml
│  ├─ sandboxg.yaml
│  ├─ browserg.yaml
│  ├─ nginx/                        common maps, shared locations, 27001, optional TLS on 27443
│  ├─ postgres/10-litellm-db.sql    initdb.d: creates the second database on an empty volume
│  └─ redis/redis.conf.tmpl         rendered with the password; noeviction + AOF
├─ images/                          base / nexus / gateway / web Dockerfiles (sandbox comes from sandboxd/)
├─ systemd/                         flame (containers), sandboxd, browserd, migrate, optional dedicated dockerd
├─ scripts/
│  ├─ lib.sh                        shared: log/die, secret generation, the config renderer
│  ├─ flame                          the wrapper installed to /usr/local/bin
│  ├─ build.sh  images.sh           products and images (images.sh also does push, and save/load for offline delivery)
│  ├─ release.sh                    one command: products + images + both tarballs + SHA256SUMS
│  ├─ install.sh                    preflight (mode-aware: nsjail included), placement, rendering + nexus.yaml, units
│  ├─ rootfs.sh                     nsjail: export an image into /opt/flame/rootfs/<tag> (+ /dev nodes, self-proof)
│  ├─ migrate.sh  migrate-apply.sh  platform SQL (ordered list, `--check` compares the file set)
│  ├─ bootstrap.sh  smoke.sh        one-shot seed (+ v1 backfill) / end-to-end call with a real answer
│  └─ dev-up.sh                     the local trial: up, seed, executor; --smoke adds the end-to-end check
├─ env/flame.env.example            every variable, what it is for, and what breaks when it changes
├─ env/SECRETS.md                   impact and rotation table (+ zh mirror)
├─ .devrun/                         a trial's rendered config and secrets (gitignored)
├─ out/                             build products (gitignored)
└─ release/                         the release bundle release.sh writes: two tarballs + SHA256SUMS (gitignored)
```

## Order of operations

```bash
# on a build machine with go, node and docker: one command, everything an offline release needs
./scripts/release.sh --tag v2026.12.01
#   deploy/prod/release/flame-prod-v2026.12.01.tar.gz    the runnable tree (products, scripts, config, units)
#   deploy/prod/release/flame-images-v2026.12.01.tar     the four runtime images + consul / postgres / redis / litellm
#   deploy/prod/release/SHA256SUMS                      verify both, on the target machine

# the same steps, one at a time
./scripts/build.sh
./scripts/images.sh build --tag v2026.12.01
./scripts/images.sh save  --tag v2026.12.01 --dir /srv/flame/dist   # offline delivery

# ... or publish the images to a registry instead of carrying a tarball
./scripts/images.sh push  --tag v2026.12.01 --registry ghcr.io/acme/flame

# on the host, as root, with the tarball: copy both files into one directory, extract the tree there
./scripts/install.sh --tag v2026.12.01 --check      # prerequisites only, changes nothing
./scripts/install.sh --tag v2026.12.01 --images /srv/flame/dist

# ... or with the registry: it pulls the four runtime images (consul / postgres / redis / litellm are pulled from
# their own publishers unless the host already has them), and records the namespace in flame.env
./scripts/install.sh --tag v2026.12.01 --check --registry ghcr.io/acme/flame
./scripts/install.sh --tag v2026.12.01 --registry ghcr.io/acme/flame

flame up -d consul postgres redis litellm          # data plane first
./scripts/migrate.sh                               # platform SQL, in dependency order
flame --profile jobs run --rm bootstrap             # plat + user01 + t0 + capabilities + API key
systemctl start flame-sandboxd flame-browserd      # the bare executors
flame up -d                                        # application plane
./scripts/health.sh
```

## Three things that bite

- **`/srv/flame/data` must be the same directory everywhere**, mounted at the same absolute path, and reached
  as uid 1000. A different path or a different uid does not fail; it silently splits tenant files into two
  trees that each look plausible.
- **The executors are not containers.** `sandboxd` needs the host docker daemon, `browserd` needs the host
  Chromium; their ports (27081 / 27091) must be reachable from the gateway containers and from the registry.
  That cross-boundary hop is configured in two places (`compose.yaml` extra_hosts, `sandboxd.yaml`
  `registry.*`), and getting one of them wrong reads as "sandboxd unreachable" or "registered but never
  healthy".
- **`FLAME_TAG` is the release.** Upgrading and rolling back are both "change that line, then `flame up -d`"
  plus a `systemctl restart` of the two bare units. Never edit files inside a running container: `flame up -d`
  after a recreate discards them.

## Where the trial and the deployment differ

| | trial (`dev-up.sh`) | deployment (`install.sh`) |
|---|---|---|
| root, systemd | no | yes (units, `flame` wrapper, `/etc/flame`) |
| storage | named Docker volume | bind mount `/srv/flame/data`, uid 1000 |
| model | seeded `DeepSeek` provider with an empty key + `deepseek-flash` | a provider you add in the console |
| executors | `sandboxd` as a local process, no `browserd` | both as systemd units |
| TLS | none | optional certificate on 27443 |
| `/health` | `ok` once the sandbox registers | must be `ok`; `health.sh` checks the rest |

## Known gaps of this drop

- **Provisioning does not create a released version**, and production entry points refuse an agent without one
  ("no released version yet"). The bootstrap job therefore runs `backfillversions` after seeding
  (`TODO(provision)` in `scripts/bootstrap.sh`): a tenant created later from the console still needs it once.
- `bootstrap` is still compiled from `nexus/cmd/seedpoc` and installed under the production name
  (`images/nexus.Dockerfile` has the note). Behaviour matches the documented contract; the rename is pending.
- There is no `schema_migrations` table and no tenant-schema upgrade tool: platform SQL is idempotent and
  applied in a fixed order, tenant schemas are not migrated at all. Tracked in `deployment.md` §11.
- No drain endpoint: stopping `sandboxd` closes its pooled containers, stopping `browserd` loses that node's
  sessions. Maintenance is "low peak, then wait for the idle timeout".
- Tier 3 (containers on separate machines) has artifacts but has not been verified end to end; the runbook's
  known-gaps section lists the boundaries.
- nsjail is an optional mode (the template ships `mode: docker`, and the `nsjail:` block is filled in so the switch is
  one line). Of the two things it needs, only one is automated: `scripts/rootfs.sh` exports the rootfs per executor per
  image generation, while the setuid `nsjail` binary still comes from a source build. [`INSTALL.md`](INSTALL.md) §4.
