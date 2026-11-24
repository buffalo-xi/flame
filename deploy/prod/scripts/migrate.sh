#!/usr/bin/env bash
# Runs the platform SQL through the `migrate` compose job.
#
#   ./scripts/migrate.sh            # apply every script, in dependency order
#   ./scripts/migrate.sh --check    # only verify that the file set matches the execution list
#
# The actual work happens inside the job (scripts/migrate-apply.sh), so the host needs no psql and the
# database never has to be reachable from here - only from the compose network.
#
# One machine runs this, never several: concurrent DDL from several nexus copies is exactly what an explicit
# one-shot job avoids.
set -euo pipefail

if [ "${1:-}" = "--check" ]; then
    shift
    exec flame --profile jobs run --rm --entrypoint /bin/sh migrate \
        /opt/flame/migrate-apply.sh --check "$@"
fi

exec flame --profile jobs run --rm migrate "$@"
