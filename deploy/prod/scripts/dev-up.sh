#!/usr/bin/env bash
# Bring the whole product up on this machine, for a trial.
#
#   ./scripts/dev-up.sh              # data plane, SQL, seed, then the application plane
#   ./scripts/dev-up.sh --smoke      # also the end-to-end check; it needs FLAME_LLM_BASE_URL, FLAME_LLM_API_KEY
#                                    # and FLAME_LLM_MODEL, and creates a provider and a model
#   ./scripts/dev-up.sh status|down|purge
#   ./scripts/dev-up.sh logs nexus
#
# What this is for: someone who has Docker and nothing else should be able to see the product work. So it needs no
# root, installs no units and writes nothing outside this directory (.devrun/ and named volumes). The seed gives the
# tenant a DeepSeek provider with an empty key: paste yours in the console, then provision its model.
#
# What this is not: an installation. Nothing here is hardened and there is no TLS; browserd is not started, so the
# browser tools report themselves unavailable (sandboxd does run, as a plain process on this host). For a real
# deployment use install.sh and the runbook.
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
prod=$(cd -- "$here/.." && pwd)
devrun=$prod/.devrun
tag=${FLAME_TAG:-dev}

# shellcheck source=scripts/lib.sh
. "$here/lib.sh"

cmd=up
# Off by default: the check creates a provider and a model under fixed names, and a trial's console should start
# empty - the first provider belongs to whoever logs in. Pass --smoke when you want that path exercised.
run_smoke=0
rest=()
for arg in "$@"; do
    case "$arg" in
        up|down|purge|status|logs) cmd=$arg ;;
        --smoke) run_smoke=1 ;;
        --no-smoke) run_smoke=0 ;;
        -h|--help) sed -n '2,/^$/p' "${BASH_SOURCE[0]}"; exit 0 ;;
        *) rest+=("$arg") ;;   # `logs <service>` passes the service names through
    esac
done

compose() {
    docker compose --env-file "$devrun/flame.env" \
        -f "$prod/compose.yaml" -f "$prod/compose.dev.yaml" "$@"
}

prepare() {
    command -v docker >/dev/null || die "docker is not installed"
    docker compose version >/dev/null 2>&1 || die "the docker compose plugin is missing"

    mkdir -p "$devrun/tls" "$devrun/logs/nexus" "$devrun/logs/sandboxg" "$devrun/logs/browserg"

    if [ ! -f "$devrun/flame.env" ]; then
        # New secrets with old volumes is the one combination that cannot work: PostgreSQL keeps the password from
        # initdb, so the stack comes up with "authentication failed" and nothing says why. Refuse instead.
        if [ -n "$(docker volume ls -q --filter name=flame-dev 2>/dev/null)" ]; then
            die "$devrun/flame.env is gone but the trial's data volumes are still there; run 'dev-up.sh purge' first"
        fi
        log "writing $devrun/flame.env (new secrets)"
        umask 077
        cat > "$devrun/flame.env" <<EOF
# Written by dev-up.sh for a local trial on $(date -u +%Y-%m-%dT%H:%M:%SZ). Not a deployment: see README.
FLAME_ENV_FILE=.devrun/flame.env
FLAME_TAG=$tag
FLAME_BIND_IP=127.0.0.1
FLAME_PUBLISH_IP=0.0.0.0
FLAME_TLS_IP=0.0.0.0
POSTGRES_PASSWORD=$(rand_hex)
REDIS_PASSWORD=$(rand_hex)
JWT_SECRET=$(rand_hex)
FLAME_VALUE_KEY=$(rand_hex)
LITELLM_MASTER_KEY=sk-$(rand_hex)
LITELLM_SALT_KEY=$(rand_hex)
SANDBOX_TOKEN=$(rand_hex)
BROWSER_TOKEN=$(rand_hex)
FLAME_ADMIN_PASSWORD=12345678
FLAME_API_KEY=sk-poc-t0-primary
FLAME_TENANT_SCHEMA=t0
FLAME_TENANT_TITLE="Default tenant"
FLAME_DB_HOST=postgres
FLAME_REDIS_ADDR=redis:6379
FLAME_LITELLM_HOST=litellm
# nexus refuses to start without a sandbox url, so the trial runs sandboxd on this machine (dev-up starts it
# below). The browser pool is not started: no browser host exists here, so the browser tools stay unavailable.
FLAME_SANDBOX_URL=ws://sandboxg:27080
FLAME_BROWSER_URL=
FLAME_CONSUL_ADDR_HOST=127.0.0.1:8500
FLAME_CONSUL_ADDR_NET=consul:8500
EOF
        chmod 0600 "$devrun/flame.env"
    fi

    set -a
    # shellcheck disable=SC1091
    . "$devrun/flame.env"
    set +a

    check_render_vars "$prod/config"
    for name in sandboxd browserd sandboxg browserg; do
        render "$prod/config/$name.yaml" "$devrun/$name.yaml" 0644
    done

    # The executor runs as this user, not as a systemd unit: its own directories have to be writable, and the
    # address it registers has to be one the gateway containers can dial (host.docker.internal reaches this host
    # from the compose network). Everything else in the rendered file is the production policy.
    state=$HOME/.flame-dev
    mkdir -p "$state/logs" "$state/tmp"
    sed -i "s|^log_dir: .*|log_dir: \"$state/logs\"|" "$devrun/sandboxd.yaml"
    sed -i "s|^tmp_dir: .*|tmp_dir: \"$state/tmp\"|" "$devrun/sandboxd.yaml"
    sed -i '/^  service_name: sandboxd$/a\  service_addr: "host.docker.internal:27081"' "$devrun/sandboxd.yaml"
    render "$prod/config/redis/redis.conf.tmpl" "$devrun/redis.conf" 0644
    # nexus expands ${VAR} itself, so its file is copied verbatim.
    cp -f "$prod/config/nexus.yaml" "$devrun/nexus.yaml"
}

require_images() {
    local missing=""
    for img in nexus gateway web base sandbox; do
        docker image inspect "flame/$img:$tag" >/dev/null 2>&1 || missing="$missing flame/$img:$tag"
    done
    if [ -n "$missing" ]; then
        cat >&2 <<EOF
dev-up.sh: missing images:$missing

On a build machine (needs go and node):
  ./scripts/build.sh
  ./scripts/images.sh build --tag $tag

Then run this again.
EOF
        exit 1
    fi
    [ -x "$prod/out/bin/sandboxd" ] || die "out/bin/sandboxd is missing: the trial runs the executor from here"
}

# sandboxd is the one process that cannot run in a container (it is the thing that starts containers), so the trial
# starts it as a plain background process and advertises host.docker.internal to the gateway containers.
start_executor() {
    if [ -f "$devrun/sandboxd.pid" ] && kill -0 "$(cat "$devrun/sandboxd.pid")" 2>/dev/null; then
        log "sandboxd already running (pid $(cat "$devrun/sandboxd.pid"))"
    else
        log "starting sandboxd"
        nohup "$prod/out/bin/sandboxd" -config "$devrun/sandboxd.yaml" >"$devrun/sandboxd.out" 2>&1 &
        echo $! >"$devrun/sandboxd.pid"
    fi
    for _ in $(seq 1 20); do
        if curl -s "http://127.0.0.1:8500/v1/health/service/sandboxd?passing=1" 2>/dev/null | grep -q '"Service"'; then
            log "sandboxd registered in consul"
            return 0
        fi
        sleep 1
    done
    warn "sandboxd did not register within 20s; see $devrun/sandboxd.out"
}

stop_executor() {
    if [ -f "$devrun/sandboxd.pid" ]; then
        kill "$(cat "$devrun/sandboxd.pid")" 2>/dev/null || true
        rm -f "$devrun/sandboxd.pid"
    fi
}

case "$cmd" in
    up)
        prepare
        require_images

        log "data plane"
        compose up -d --wait consul postgres redis litellm

        log "volume ownership"
        compose --profile jobs run --rm dev-fix-perms

        log "platform SQL"
        compose --profile jobs run --rm migrate

        log "seeding platform accounts and tenant ${FLAME_TENANT_SCHEMA}"
        compose --profile jobs run --rm bootstrap

        start_executor

        log "application plane"
        # No --wait for nexus: its readiness probe is "unavailable" until a sandbox executor registers, which a
        # trial deliberately does not run. See compose.dev.yaml.
        compose up -d nexus sandboxg browserg web
        # Wait for readiness, not for the console: the SPA is served from disk and answers 200 long before the API
        # can. /health is proxied to nexus and stays 503 until every dependency, including the sandbox capability
        # snapshot, is in place - which is exactly the condition a first call needs.
        code=000
        for _ in $(seq 1 60); do
            code=$(curl -s -o /dev/null -w '%{http_code}' -m 2 http://127.0.0.1:27001/health 2>/dev/null) || code=000
            [ "$code" = "200" ] && break
            sleep 2
        done
        [ "$code" = "200" ] || warn "the API is still not ready (http $code); check ./scripts/dev-up.sh logs nexus"

        if [ "$run_smoke" -eq 1 ]; then
            log "smoke test (creates a provider and a model under fixed names)"
            "$here/smoke.sh" --entry http://127.0.0.1:27001 || true
        fi

        cat <<EOF

Flame is running.

  console        http://127.0.0.1:27001
  platform admin plat / ${FLAME_ADMIN_PASSWORD}
  tenant ${FLAME_TENANT_SCHEMA}      primary / ${FLAME_ADMIN_PASSWORD}  (choose tenant ${FLAME_TENANT_SCHEMA} on the login screen)

  credentials    .devrun/initial-credentials.txt
  logs           ./scripts/dev-up.sh logs nexus
  stop           ./scripts/dev-up.sh down      (keeps data)
  wipe           ./scripts/dev-up.sh purge     (deletes data)

The seed ships one provider, DeepSeek, with an empty API key: paste your key under Model providers, then provision
its deepseek-flash model.
EOF
        ;;

    down)
        prepare
        stop_executor
        compose down
        log "stopped; data volumes kept (use 'purge' to delete them)"
        ;;

    purge)
        prepare
        stop_executor
        compose down -v
        rm -rf "$devrun"
        log "stopped; volumes and .devrun/ deleted"
        ;;

    status)
        prepare
        compose ps
        ;;

    logs)
        prepare
        compose logs -f --tail=100 "${rest[@]}"
        ;;

    *)
        die "unknown command: $cmd (up|down|purge|status|logs)"
        ;;
esac
