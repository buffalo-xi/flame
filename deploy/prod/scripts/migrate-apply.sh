#!/bin/sh
# Applies the platform SQL, in dependency order, inside the `migrate` job (postgres image, psql only).
#
#   docker compose ... run --rm migrate                  # apply
#   docker compose ... run --rm --entrypoint /bin/sh migrate /opt/flame/migrate-apply.sh --check
#
# The list below is the contract, and it is the same list scripts/verify-sql.ps1 executes:
#
#   * order matters, alphabetical does not work (the asset_* scripts need shared_assets first);
#   * the file set must be *equal* to this list, not a subset: a script that exists in nexus/platform/sql but
#     never runs leaves an existing database on the old schema while the new code runs against it, and the only
#     symptom is a generic error in the console. --check enforces that equality before anything is applied.
#
# Why SQL and not a Go migration tool: these files are idempotent by design (CREATE ... IF NOT EXISTS, guarded
# INSERT), and they are the same text verify-sql.ps1 validates against a throwaway database. There is no version
# table yet - that is the known gap tracked in nexus/docs/deployment.md section 11.
set -eu

SQL_DIR=${SQL_DIR:-/sql}

files="
platform.sql
quota_storage.sql
flow_configs.sql
agent_configs.sql
llm_provider_presets.sql
llm_presets_seed.sql
tenant_capabilities.sql
capability_gates.sql
capability_code_rename.sql
shared_assets.sql
asset_listing_rename.sql
asset_build_rename.sql
asset_pack_digest.sql
listing_policy.sql
peer_market.sql
plugin_gates.sql
tenant_plugins.sql
customtool_capability_exec.sql
flow_run_identity.sql
agent_run_identity.sql
agent_run_session_index.sql
zz_limit_defaults.sql
"

mode=apply
[ "${1:-}" = "--check" ] && mode=check

missing=""
for f in $files; do
    [ -f "$SQL_DIR/$f" ] || missing="$missing $f"
done

unlisted=""
for p in "$SQL_DIR"/*.sql; do
    [ -e "$p" ] || continue
    b=$(basename "$p")
    case " $(echo $files) " in
        *" $b "*) ;;
        *) unlisted="$unlisted $b" ;;
    esac
done

count=$(echo $files | wc -w)

if [ -n "$missing" ] || [ -n "$unlisted" ]; then
    echo "migrate: the SQL file set does not match the execution list" >&2
    [ -n "$missing" ]  && echo "  listed but absent:$missing" >&2
    [ -n "$unlisted" ] && echo "  present but never applied:$unlisted" >&2
    echo "  add the file to scripts/migrate-apply.sh (and to scripts/verify-sql.ps1), or remove it from both" >&2
    exit 1
fi

if [ "$mode" = check ]; then
    echo "migrate: $count scripts, file set matches the execution list"
    exit 0
fi

echo "migrate: applying $count scripts from $SQL_DIR"
psql -q -v ON_ERROR_STOP=1 -c 'CREATE SCHEMA IF NOT EXISTS platform'

for f in $files; do
    printf '  %s\n' "$f"
    psql -q -v ON_ERROR_STOP=1 -f "$SQL_DIR/$f"
done

echo "migrate: done"
