#!/bin/sh
# The one-shot seeding job: `flame --profile jobs run --rm bootstrap`.
#
# It runs the seeder that ships in the nexus image (equivalence with the demo box's seedpoc is the point: the
# same code path creates the tenant that the API would create, so "verified in the PoC" transfers), then writes
# the credentials to a 0600 file instead of leaving them only in a terminal scrollback.
#
# Everything it does is idempotent. Running it a second time only fills in what is missing, so it is safe to
# re-run after a half-finished first start - and it must not be part of a normal upgrade, where it would only
# print credentials nobody asked for.
set -eu

config=${FLAME_CONFIG:-/etc/flame/nexus.yaml}
schema=${FLAME_TENANT_SCHEMA:-t0}
title=${FLAME_TENANT_TITLE:-Default tenant}
bin=/opt/flame/bin/bootstrap

: "${FLAME_ADMIN_PASSWORD:?FLAME_ADMIN_PASSWORD is required (seed credential for plat, user01 and the tenant administrator)}"
# No apostrophe in these messages: inside ${VAR:?...} a single quote starts a quoted section and breaks the file.
: "${FLAME_API_KEY:?FLAME_API_KEY is required (API key of the primary user of the tenant)}"

[ -f "$config" ] || { echo "bootstrap: no configuration at $config" >&2; exit 1; }

echo "bootstrap: seeding platform accounts and tenant '$schema'"
log=/tmp/bootstrap.log
if ! "$bin" -config "$config" -schema "$schema" -password "$FLAME_ADMIN_PASSWORD" \
        -apikey "$FLAME_API_KEY" -title "$title" >"$log" 2>&1; then
    echo "bootstrap: seeding failed; output follows" >&2
    cat "$log" >&2
    exit 1
fi
cat "$log"

# Production entry points refuse to run an agent that has no released version ("no released version yet"), and
# provisioning does not create one - so the first tenant of a fresh install would be reachable but unusable. This
# is the same program the upgrade path uses, and it is idempotent: only entities without a version row are touched.
#
# TODO(provision): a tenant should get its v1 at creation time. Until that lands, this call is the patch that
# makes "install, then use" work, and it is why the binary ships in the image at all.
if [ -x /opt/flame/bin/backfillversions ]; then
    echo "bootstrap: releasing v1 for entities that have no version row"
    # The tool hardcodes ./config.yaml (like nexus), so give it a working directory where that name exists. A
    # symlink in a temp dir, rather than a copy: the file is a few hundred bytes and links survive rotation.
    tmpdir=$(mktemp -d)
    ln -sf "$config" "$tmpdir/config.yaml"
    ( cd "$tmpdir" && /opt/flame/bin/backfillversions -schema "$schema" ) \
        || echo "bootstrap: the backfill reported errors (above); calls touching those entities will fail" >&2
    rm -rf "$tmpdir"
fi

# 0600 and owned by the user the job runs as (uid 1000): the file holds the seed password and the API key until
# someone rotates them. Delete it once they are in a vault.
creds=/etc/flame/initial-credentials.txt
umask 077
cat > "$creds" <<EOF
# Written by the bootstrap job on $(date -u +%Y-%m-%dT%H:%M:%SZ).
# Delete this file once the password is changed and the API key is rotated.

platform administrator : plat / ${FLAME_ADMIN_PASSWORD}
platform user (owner)  : user01 / ${FLAME_ADMIN_PASSWORD}
tenant                 : ${schema}
tenant administrator   : primary / ${FLAME_ADMIN_PASSWORD}
api key (primary user) : ${FLAME_API_KEY}
EOF
chmod 0600 "$creds"

echo "bootstrap: credentials written to $creds (0600)"
if [ "${1:-}" = "--print" ]; then
    cat "$creds"
else
    echo "bootstrap: pass --print to show them here"
fi
