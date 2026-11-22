#!/bin/sh
#
# Bootstrap for the Flame container of the PoC, then hand over to supervisord (PID 1). The LLM gateway is a separate
# container; this side only owns the product stack (see docker-compose.yml).
#
# Why the ordering lives here and not in supervisord: supervisord starts programs in parallel and knows nothing about
# readiness. Creating a cluster, creating the databases and applying the platform SQL has to happen exactly once, before
# nexus or litellm open a connection — so it runs here, synchronously, and supervisord only starts already-satisfiable
# services afterwards.
#
# Everything below is idempotent: a second `docker start` skips initdb (PG_VERSION exists) and the SQL (the flame
# database exists). The administrator seed (90-admin.sql) and the demo-environment seeder (seedpoc) run on every boot:
# both only fill in what is missing, and doing so repairs a half-finished first run.

set -eu

DATA_DIR=/data
PGDATA="$DATA_DIR/pg"
SQL_DIR=/opt/flame/sql
PROFILE_DIR=/var/lib/flame/profiles
BOOTSTRAP_PG_LOG=/var/log/flame/bootstrap-postgres.log

log() {
    printf '%s entrypoint: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >&2
}

# ---------------------------------------------------------------------------
# 1) Overlay prebuilt artifacts, when the host mounted them.
#
# The reason for the copy instead of mounting straight onto the real paths: Windows bind mounts present every file as
# 0777 and silently ignore chmod, so a mounted binary can end up non-executable. Copying into the image's own writable
# layer and chmod-ing there is the only reliable way, and it doubles as "partial override" — whatever is in the mount
# wins, everything else keeps the build baked into the image.
# ---------------------------------------------------------------------------
if [ -d /opt/flame/bin-mount ]; then
    log "overlaying prebuilt binaries from /opt/flame/bin-mount"
    # "Whatever is in the mount wins" also means "whatever is missing silently keeps the image's copy", which is how a
    # freshly built nexus ends up driving an older seedpoc. Name the gap instead of hiding it.
    for b in nexus seedpoc sandboxd codehost browserd browserhost; do
        if [ ! -f "/opt/flame/bin-mount/$b" ]; then
            log "WARNING: $b is missing from the bin mount; the image's copy stays in place"
        fi
    done
    cp -a /opt/flame/bin-mount/. /opt/flame/bin/
    chmod 0755 /opt/flame/bin/* 2>/dev/null || true
fi

if [ -d /usr/share/nginx/html-mount ] && [ -n "$(ls -A /usr/share/nginx/html-mount 2>/dev/null)" ]; then
    # Copy over instead of replacing the directory. The writable layer survives `docker restart`, so a `rm -rf` here would
    # make "ran once against a half-built out/dist" permanent: the image's bundle is gone and only a new container brings
    # it back. Stale hashed assets left behind are harmless; an empty mount is skipped for the same reason.
    log "overlaying the desktop bundle from /usr/share/nginx/html-mount"
    cp -a /usr/share/nginx/html-mount/. /usr/share/nginx/html/
    chmod -R a+rX /usr/share/nginx/html
fi

# ---------------------------------------------------------------------------
# 2) Directories
# ---------------------------------------------------------------------------
mkdir -p "$PGDATA" "$PROFILE_DIR" /var/log/flame /var/lib/flame /tmp/flame-sandbox /run
chown -R postgres:postgres "$PGDATA" 2>/dev/null || true

# ---------------------------------------------------------------------------
# 3) Postgres cluster
# ---------------------------------------------------------------------------
if [ ! -s "$PGDATA/PG_VERSION" ]; then
    log "initialising a new postgres cluster in $PGDATA"
    /usr/local/bin/flame-gosu postgres /usr/local/bin/flame-initdb \
        -D "$PGDATA" -A trust -E UTF8 --locale=C >/dev/null
fi

# A data directory belongs to the major that created it: 18 cannot open 17's files, and postgres' own complaint about
# that arrives at the bottom of a page of startup noise. One sentence here, before anything starts.
# The version string is "postgres (PostgreSQL) 18.0 (Debian ...)" — field 3 is the version, its first part the major.
if [ -s "$PGDATA/PG_VERSION" ]; then
    volume_major="$(cat "$PGDATA/PG_VERSION")"
    image_major="$(/usr/local/bin/flame-pg --version | cut -d' ' -f3 | cut -d. -f1)"
    if [ "$volume_major" != "$image_major" ]; then
        log "the data volume was created by postgres $volume_major, this image ships $image_major"
        log "a data directory cannot be opened by another major: start from a fresh volume (docker compose down -v), or run pg_upgrade against it"
        exit 1
    fi
fi

# The LiteLLM container reaches this cluster over the compose network, and initdb's default pg_hba.conf only admits
# 127.0.0.1/32 and ::1/128. One trust line for the compose subnet is what lets that peer in; nothing is published to the
# host, so the reachable set stays "containers on this compose project".
# Guarded so a restart does not append the line again.
if ! grep -qx 'host all all 0.0.0.0/0 trust' "$PGDATA/pg_hba.conf"; then
    log "allowing the compose network into postgres"
    echo 'host all all 0.0.0.0/0 trust' >>"$PGDATA/pg_hba.conf"
fi

log "starting postgres for the bootstrap step"
/usr/local/bin/flame-gosu postgres /usr/local/bin/flame-pg -D "$PGDATA" \
    -c listen_addresses=0.0.0.0 -c port=5432 -c unix_socket_directories=/tmp \
    >>"$BOOTSTRAP_PG_LOG" 2>&1 &
PG_PID=$!

ready=0
i=0
while [ "$i" -lt 60 ]; do
    if pg_isready -h 127.0.0.1 -p 5432 -U postgres >/dev/null 2>&1; then
        ready=1
        break
    fi
    i=$((i + 1))
    sleep 1
done
if [ "$ready" -ne 1 ]; then
    log "postgres did not become ready within 60s; last log lines:"
    tail -n 20 "$BOOTSTRAP_PG_LOG" >&2 || true
    kill -INT "$PG_PID" 2>/dev/null || true
    exit 1
fi

# ---------------------------------------------------------------------------
# 4) Databases and platform SQL
#
# The script order is the dependency order from scripts/verify-sql.ps1 (alphabetical does not work: the asset_* scripts
# need shared_assets first). Every script is applied on every boot: they are all idempotent, and re-running them is the
# only thing that carries an existing /data volume across an image upgrade.
#
# The invariant that makes this safe: **everything in the SQL directory must be correct when re-run**. A statement that
# is only right once - a data fix-up tied to an older shape, like the builtin:exec -> builtin:shell/builtin:script
# expansion that used to sit in tenant_capabilities.sql - must not live here: it turned into one dirty row per boot until
# it was removed.
# ---------------------------------------------------------------------------
export PGHOST=127.0.0.1 PGPORT=5432 PGUSER=postgres

# The gateway's own database. The LiteLLM container creates its tables itself (Prisma, on first start); this only has to
# exist, and it has to exist before that container starts retrying.
if ! psql -tAc "SELECT 1 FROM pg_database WHERE datname='litellm'" | grep -q 1; then
    log "creating the litellm database"
    psql -q -v ON_ERROR_STOP=1 -d postgres -c "CREATE DATABASE litellm"
fi

# Creating the flame database is a one-off, applying the platform SQL is not. The old gate here was `datname='flame'`,
# which meant an upgraded image never ran the newer scripts on an existing volume: CREATE TABLE IF NOT EXISTS cannot add
# a missing table to a database that already passed the gate, and the first symptom was a generic "internal error" in
# the console (platform.flow_configs / agent_configs / llm_provider_presets were missing outright).
if ! psql -tAc "SELECT 1 FROM pg_database WHERE datname='flame'" | grep -q 1; then
    log "creating the flame database"
    psql -q -v ON_ERROR_STOP=1 -d postgres -c "CREATE DATABASE flame"
fi

psql -q -v ON_ERROR_STOP=1 -d flame -c "CREATE SCHEMA IF NOT EXISTS platform"
for f in "$SQL_DIR"/*.sql; do
    case "$f" in
        */90-admin.sql) continue ;;   # applied on every boot, below
    esac
    psql -q -v ON_ERROR_STOP=1 -d flame -f "$f"
done
log "platform SQL applied"

if [ -f "$SQL_DIR/90-admin.sql" ]; then
    psql -q -v ON_ERROR_STOP=1 -d flame -f "$SQL_DIR/90-admin.sql"
fi

# ---------------------------------------------------------------------------
# 5) Demo environment: the owner account, the tenant (created through the same provisioning path the API uses) and an
# apikey bound to the tenant's primary user.
#
# It runs here rather than from supervisord because nexus creates no account and no tenant on startup — the tenant has
# to exist before the first login — and because the platform SQL must already be applied. Postgres is up at this point;
# the seeder is idempotent, so on a restart it only repairs what is missing.
# ---------------------------------------------------------------------------
log "seeding the demo environment"
/opt/flame/bin/seedpoc -config /etc/flame/nexus/config.yaml 1>&2

log "bootstrap complete"

# ---------------------------------------------------------------------------
# 6) Stop the bootstrap server; supervisord starts the real one and keeps it alive.
# ---------------------------------------------------------------------------
kill -INT "$PG_PID"
wait "$PG_PID" 2>/dev/null || true

# ---------------------------------------------------------------------------
# 7) Hand over. $@ is the CMD (supervisord -n -c ...).
# ---------------------------------------------------------------------------
log "starting supervisord"
exec "$@"
