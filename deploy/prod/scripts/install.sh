#!/usr/bin/env bash
# Place a release on this machine: products, compose files, units, configuration, the `flame` wrapper.
#
#   sudo ./scripts/install.sh --tag 2026.12.01 --check   # prerequisites only, changes nothing
#   sudo ./scripts/install.sh --tag 2026.12.01           # place the release and the units, images already local
#   sudo ./scripts/install.sh --tag 2026.12.01 --images /srv/flame/dist
#   sudo ./scripts/install.sh --tag 2026.12.01 --registry ghcr.io/acme/flame
#
# Idempotent: running it twice with the same tag only refreshes files. It never touches /etc/flame/flame.env,
# the rendered service configurations or /srv/flame/data once they exist - those carry host policy, secrets and
# tenant data, and an upgrade must not overwrite them. Pass --force-config to re-render the four generated
# configurations from the release's templates (it overwrites the files, nowhere else). --registry is the one thing
# that does edit flame.env (the FLAME_IMAGE_NS line): compose has to ask for the same names it pulled.
#
# It does not start anything: starting is explicit, and the order matters (data plane, migrate, bootstrap,
# executors, application plane). See the runbook, section 7.
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
prod=$(cd -- "$here/.." && pwd)
from=$prod/out
tag="${FLAME_TAG:-}"
images_dir=""
registry=""
check_only=0
force_config=0

# shellcheck source=scripts/lib.sh
. "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

while [ $# -gt 0 ]; do
    case "$1" in
        --tag)          tag=${2:?--tag needs a value}; shift 2 ;;
        --from)         from=${2:?--from needs a value}; shift 2 ;;
        --images)       images_dir=${2:?--images needs a value}; shift 2 ;;
        --registry)     registry=${2:?--registry needs a value}; shift 2 ;;
        --check)        check_only=1; shift ;;
        --force-config) force_config=1; shift ;;
        -h|--help)      sed -n '2,16p' "${BASH_SOURCE[0]}"; exit 0 ;;
        *)              die "unknown argument: $1" ;;
    esac
done

[ -n "$tag" ] || die "--tag is required (or set FLAME_TAG)"
[ "$(id -u)" -eq 0 ] || die "run as root (it creates users, directories and systemd units)"
[ -z "$registry" ] || [ -z "$images_dir" ] \
    || die "--registry and --images are two delivery paths: pass one (a tarball already carries the images)"

# The namespace every image reference resolves through, and the one written into flame.env by --registry.
if [ -n "$registry" ]; then
    FLAME_IMAGE_NS=${registry%/}
    export FLAME_IMAGE_NS
fi


# ---------------------------------------------------------------------------
# Checks. --check runs exactly this and stops.
# ---------------------------------------------------------------------------
failed=0
ok()   { printf '  \033[32mok\033[0m    %s\n' "$*"; }
bad()  { printf '  \033[31mFAIL\033[0m  %s\n' "$*"; failed=$((failed + 1)); }
note() { printf '  \033[33mwarn\033[0m  %s\n' "$*"; }

log "checking prerequisites"
command -v docker >/dev/null || die "docker is not installed"
if docker info >/dev/null 2>&1; then ok "docker daemon reachable"; else bad "docker daemon not reachable"; fi
if docker compose version >/dev/null 2>&1; then ok "compose plugin present"; else bad "docker compose plugin missing"; fi
command -v openssl >/dev/null && ok "openssl present" || bad "openssl missing (secret generation)"

if id flame >/dev/null 2>&1; then
    got=$(id -u flame)
    if [ "$got" = 1000 ]; then
        ok "user flame (uid 1000)"
    else
        bad "user flame has uid $got; the sandbox runs as 1000:1000 and shares ${FLAME_STORAGE_ROOT:-/srv/flame/data} with it"
    fi
elif getent passwd 1000 >/dev/null; then
    bad "uid 1000 belongs to '$(getent passwd 1000 | cut -d: -f1)'; create a 'flame' user with uid 1000 or re-map that account first"
else
    ok "uid 1000 is free (install.sh will create user flame)"
fi

if [ -n "$images_dir" ]; then
    ls "$images_dir"/*.tar >/dev/null 2>&1 && ok "image bundle in $images_dir" || bad "no *.tar in $images_dir"
    note "the images inside the bundle are checked after loading, not here"
elif [ -n "$registry" ]; then
    # Resolve, do not pull: --check changes nothing, and this is exactly the question a registry delivery cannot answer
    # later without a twenty-minute pull first. A manifest miss here is "wrong tag", "wrong namespace" or "not logged
    # in", and all three are cheap to fix now.
    for img in $(flame_runtime_images "$FLAME_IMAGE_NS" "$tag"); do
        if docker manifest inspect "$img" >/dev/null 2>&1; then
            ok "registry image $img"
        else
            bad "cannot resolve $img (docker login, wrong --registry, or the tag was never pushed)"
        fi
    done
    note "consul / postgres / redis / litellm still come from their own publishers unless the host has them"
else
    # Everything compose can ask for, upstream included: a missing postgres or litellm image is not a pull error at
    # this point, it is "the container exited immediately" ten minutes later.
    for img in $(required_images "$prod" "$FLAME_IMAGE_NS" "$tag"); do
        if docker image inspect "$img" >/dev/null 2>&1; then
            ok "image $img"
        else
            bad "image $img missing (build it, pass --images <dir>, or --registry <ns>)"
        fi
    done
fi

if [ -f "$from/bin/sandboxd" ] || [ -d /opt/flame/releases/"$tag"/bin ]; then
    ok "release products for $tag"
else
    bad "no products for $tag (./scripts/build.sh, or --from <dir>)"
fi

# A real mount is the difference between "shared storage" and "two local disks with the same path", which
# fails as files that appear and disappear instead of as an error. A local directory is correct on tier 1 and
# wrong the moment a second machine runs nexus, browserd or nginx.
if [ ! -d /srv/flame/data ]; then
    note "/srv/flame/data does not exist yet (install.sh creates it)"
elif command -v findmnt >/dev/null && findmnt -T /srv/flame/data >/dev/null 2>&1; then
    ok "/srv/flame/data is a mount point"
else
    note "/srv/flame/data is a plain directory - correct for one machine, wrong as soon as a second node serves files from it"
fi

for p in 27001 27443 8500 27081 27091; do
    if ss -ltn "sport = :$p" 2>/dev/null | grep -q LISTEN; then
        note "port $p already in use - expected on an upgrade, a conflict on a first install"
    fi
done

if command -v chromium >/dev/null || command -v chromium-browser >/dev/null || command -v google-chrome >/dev/null; then
    ok "a Chromium binary is on PATH"
else
    note "no Chromium on PATH: the browser tools will report 'unavailable' (needed by browserd)"
fi

if command -v nsjail >/dev/null; then
    ok "nsjail present (only needed for mode: nsjail)"
else
    note "nsjail absent - fine for the default mode: docker"
fi

if [ "$failed" -gt 0 ]; then
    die "$failed prerequisite(s) failed"
fi
if [ "$check_only" -eq 1 ]; then
    log "prerequisites ok (nothing was changed)"
    exit 0
fi

# ---------------------------------------------------------------------------
# User and directories
# ---------------------------------------------------------------------------
if ! id flame >/dev/null 2>&1; then
    log "creating user flame (uid 1000)"
    useradd -u 1000 -U -m -d /var/lib/flame -s /usr/sbin/nologin flame
fi

log "creating directories"
install -d -m 0755 /opt/flame/releases /opt/flame/rootfs
install -d -m 0750 /etc/flame /etc/flame/tls
# Pre-created so the bootstrap job (uid 1000, no write permission on a 0750 root:flame directory) can fill it in:
# the file itself is group-writable, the directory stays closed.
install -m 0660 -o root -g flame /dev/null /etc/flame/initial-credentials.txt
install -d -m 0755 /srv/flame/pg-init
install -d -m 0755 -o flame -g flame /srv/flame/data /srv/flame/data/tenants \
    /srv/flame/data/platform-assets /srv/flame/data/plugins
install -d -m 0755 -o flame -g flame /var/lib/flame/sandboxd /var/lib/flame/sandboxd/tmp \
    /var/lib/flame/browserd /var/lib/flame/browserd/profiles /var/lib/flame/nexus
for d in nexus sandboxg browserg sandboxd browserd; do
    install -d -m 0755 -o flame -g flame "/var/log/flame/$d"
done

# ---------------------------------------------------------------------------
# Release directory
# ---------------------------------------------------------------------------
release=/opt/flame/releases/$tag
log "placing the release in $release"
install -d "$release"
for d in bin static web sql; do
    if [ -d "$from/$d" ]; then
        rm -rf "${release:?}/$d"
        cp -a "$from/$d" "$release/$d"
    elif [ ! -d "$release/$d" ]; then
        [ "$d" = web ] || die "$from/$d is missing (./scripts/build.sh)"
    fi
done

cp -f "$prod/compose.yaml" "$prod/compose.multi-host.yaml" "$release/"
cp -a "$prod/scripts" "$release/scripts"
cp -a "$prod/systemd" "$release/systemd"
cp -a "$prod/config" "$release/config"
cp -f "$prod/README.md" "$prod/README.zh.md" "$release/" 2>/dev/null || true
find "$release/scripts" -name '*.sh' -exec chmod 0755 {} +
chmod 0755 "$release/scripts/flame"

ln -sfn "$release" /opt/flame/current

# ---------------------------------------------------------------------------
# Images: pulled from a registry, or loaded from a tarball
# ---------------------------------------------------------------------------
if [ -n "$registry" ]; then
    log "pulling images from $FLAME_IMAGE_NS"
    # Only the runtime images: the base is build-time, and the final images already carry its layers.
    for img in $(flame_runtime_images "$FLAME_IMAGE_NS" "$tag"); do
        docker pull "$img"
    done
elif [ -n "$images_dir" ]; then
    log "loading images from $images_dir"
    "$release/scripts/images.sh" load --dir "$images_dir"
fi

# Whichever path ran, the host has to end up holding the whole set: compose asks for these by name and has no fallback,
# so a gap is reported here instead of as a container that exits during startup.
missing=""
for img in $(required_images "$prod" "$FLAME_IMAGE_NS" "$tag"); do
    docker image inspect "$img" >/dev/null 2>&1 || missing="$missing $img"
done
[ -z "$missing" ] \
    || die "images missing after delivery:$missing (pull them, or put them in the bundle)"

# ---------------------------------------------------------------------------
# Convention: /etc/flame/flame.env
# ---------------------------------------------------------------------------
if [ ! -f /etc/flame/flame.env ]; then
    log "generating /etc/flame/flame.env (new secrets)"
    umask 077
    cat > /etc/flame/flame.env <<EOF
# Written by install.sh on $(date -u +%Y-%m-%dT%H:%M:%SZ). Mode 0600: it holds every secret of this install.
# Changing FLAME_TAG is the whole upgrade/rollback procedure.

FLAME_TAG=$tag

# Where the four Flame images live: `flame` for a release built on this host, a registry prefix (install.sh
# --registry) for one pulled from it. compose.yaml and sandboxd.yaml both read this value, so the three agree.
FLAME_IMAGE_NS=${FLAME_IMAGE_NS:-flame}

# Tier 1 (everything on this host): the executors register over loopback.
# Tier 2 (extra executor machines): set this machine's intranet IP and FLAME_CONSUL_ADDR_HOST to it.
FLAME_BIND_IP=${FLAME_BIND_IP:-127.0.0.1}

# compose.multi-host.yaml is loaded only by tier 3.
COMPOSE_FILE=${COMPOSE_FILE:-compose.yaml}

# Where 27001 / 27443 are published. Behind an upstream load balancer, set these to the intranet IP (or
# 127.0.0.1) - full capability means a plaintext Bearer token on the wire.
FLAME_PUBLISH_IP=${FLAME_PUBLISH_IP:-0.0.0.0}
FLAME_TLS_IP=${FLAME_TLS_IP:-0.0.0.0}

POSTGRES_PASSWORD=$(rand_hex)
REDIS_PASSWORD=$(rand_hex)
JWT_SECRET=$(rand_hex)
FLAME_VALUE_KEY=$(rand_hex)
LITELLM_MASTER_KEY=sk-$(rand_hex)
# Never rotate this one: it decrypts the provider keys stored in LiteLLM's database.
LITELLM_SALT_KEY=$(rand_hex)
SANDBOX_TOKEN=$(rand_hex)
BROWSER_TOKEN=$(rand_hex)

# The seed credentials of the first installation. They are the PoC values on purpose (same seed content as the
# demo box, and vendor provisioning matches it) - change the password after the first login and rotate the API
# key, see README.
FLAME_ADMIN_PASSWORD=${FLAME_ADMIN_PASSWORD:-12345678}
FLAME_API_KEY=${FLAME_API_KEY:-sk-poc-t0-primary}
FLAME_TENANT_SCHEMA=${FLAME_TENANT_SCHEMA:-t0}
# Quoted: the file is sourced by the `flame` wrapper, and an unquoted value with a space runs "tenant" as a command.
FLAME_TENANT_TITLE="${FLAME_TENANT_TITLE:-Default tenant}"

# Data plane as seen from the containers. Tier 3: the first machine's intranet address.
FLAME_DB_HOST=${FLAME_DB_HOST:-postgres}
FLAME_REDIS_ADDR=${FLAME_REDIS_ADDR:-redis:6379}
FLAME_LITELLM_HOST=${FLAME_LITELLM_HOST:-litellm}
# Full URLs of the two gateways (nexus dials them; the executors sit behind them). Empty means "no executor":
# nexus then skips the sandbox readiness gate. Never leave these empty on a real deployment.
FLAME_SANDBOX_URL=${FLAME_SANDBOX_URL:-ws://sandboxg:27080}
FLAME_BROWSER_URL=${FLAME_BROWSER_URL:-ws://browserg:27090}

# Consul, two views of the same registry:
#   _HOST - from the host-side executors (tier 1: the loopback publish; tier 2/3: this machine's IP)
#   _NET  - from the gateway containers (service name, or the first machine's IP in tier 3)
FLAME_CONSUL_ADDR_HOST=${FLAME_CONSUL_ADDR_HOST:-127.0.0.1:8500}
FLAME_CONSUL_ADDR_NET=${FLAME_CONSUL_ADDR_NET:-consul:8500}
EOF
    chmod 0600 /etc/flame/flame.env
else
    log "/etc/flame/flame.env exists, keeping it"
    if ! grep -qx "FLAME_TAG=$tag" /etc/flame/flame.env; then
        warn "it pins FLAME_TAG=$(sed -n 's/^FLAME_TAG=//p' /etc/flame/flame.env | head -n1); the release just placed is $tag"
        warn "the stack keeps running the old tag until that line is changed (that is the rollback mechanism)"
    fi
    # The one line --registry is allowed to write: `flame up -d` interpolates the namespace from this file, so a pull
    # that is not recorded here is undone by the next start (compose would ask for flame/nexus again).
    if [ -n "$registry" ]; then
        current=$(sed -n 's/^FLAME_IMAGE_NS=//p' /etc/flame/flame.env | head -n1)
        if [ "$current" != "$FLAME_IMAGE_NS" ]; then
            log "recording FLAME_IMAGE_NS=$FLAME_IMAGE_NS in /etc/flame/flame.env"
            if grep -q '^FLAME_IMAGE_NS=' /etc/flame/flame.env; then
                sed -i "s|^FLAME_IMAGE_NS=.*|FLAME_IMAGE_NS=$FLAME_IMAGE_NS|" /etc/flame/flame.env
            else
                printf '\n# Written by install.sh --registry; compose and sandboxd.yaml read it.\nFLAME_IMAGE_NS=%s\n' \
                    "$FLAME_IMAGE_NS" >> /etc/flame/flame.env
            fi
        fi
    fi
fi

set -a
# shellcheck disable=SC1091
. /etc/flame/flame.env
set +a

# ---------------------------------------------------------------------------
# Rendered configuration. Only the four files that do not expand ${VAR} themselves, plus redis.conf.
# ---------------------------------------------------------------------------
# render() comes from lib.sh. Mode 0640 root:flame: the executor and gateway files carry SANDBOX_TOKEN /
# BROWSER_TOKEN / the Redis password, and the unit reads them as user flame.
check_render_vars "$release/config"

for name in sandboxd browserd sandboxg browserg; do
    dst=/etc/flame/"$name".yaml
    if [ -f "$dst" ] && [ "$force_config" -eq 0 ]; then
        log "$dst exists, keeping it (--force-config re-renders)"
    else
        log "rendering $dst"
        render "$release/config/$name.yaml" "$dst" 0640
    fi
done

if [ -f /etc/flame/redis.conf ] && [ "$force_config" -eq 0 ]; then
    log "/etc/flame/redis.conf exists, keeping it"
else
    log "rendering /etc/flame/redis.conf"
    render "$release/config/redis/redis.conf.tmpl" /etc/flame/redis.conf 0640
fi

# /etc/flame is 0750 root:root; the files inside have to be readable by the unit's user.
chown root:flame /etc/flame/*.yaml /etc/flame/redis.conf

install -m 0644 "$release/config/postgres/10-litellm-db.sql" /srv/flame/pg-init/10-litellm-db.sql

# ---------------------------------------------------------------------------
# Wrapper and units
# ---------------------------------------------------------------------------
log "installing /usr/local/bin/flame"
install -m 0755 "$release/scripts/flame" /usr/local/bin/flame

log "installing systemd units"
install -m 0644 "$release/systemd/"*.service /etc/systemd/system/
systemctl daemon-reload
# The executors and the container stack are enabled, not started: the order in section 7 of the runbook is the
# installation order, and a half-installed database is worse than a stopped stack.
systemctl enable flame.service flame-sandboxd.service flame-browserd.service >/dev/null

log "release $tag placed"
cat <<EOF

Next (runbook section 7):
  flame up -d consul postgres redis litellm
  flame --profile jobs run --rm migrate
  flame --profile jobs run --rm bootstrap
  systemctl start flame-sandboxd flame-browserd
  flame up -d
  ./scripts/health.sh

Credentials: /etc/flame/initial-credentials.txt (written by the bootstrap job, mode 0600).
EOF
