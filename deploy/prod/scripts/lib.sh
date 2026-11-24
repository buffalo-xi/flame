#!/usr/bin/env bash
# Helpers shared by install.sh and dev-up.sh. Sourced, never executed.
#
# The only thing worth keeping in one place is render(): the executor/gateway configurations are the one set of
# files whose placeholder set has to stay in step with what the services expect, and two copies of that list is
# how a new placeholder ends up empty in one path and correct in the other.
#
# Callers must have exported the variables before calling render() (install.sh and dev-up.sh both load
# /etc/flame/flame.env or .devrun/flame.env first).

log()  { printf '\033[36m==> %s\033[0m\n' "$*"; }
warn() { printf '\033[33m    warning: %s\033[0m\n' "$*"; }
die()  { printf '%s: %s\n' "${0##*/}" "$*" >&2; exit 1; }

# Hex, not base64: this value lands inside a URL (the LiteLLM DSN) and inside shell assignments, where base64's
# +, / and = would need escaping in both.
rand_hex() { openssl rand -hex 24; }

# Variables a template may reference. Keep in step with config/sandboxd.yaml, browserd.yaml, sandboxg.yaml,
# browserg.yaml and redis/redis.conf.tmpl - dev-up.sh checks the set and fails loudly on an unlisted placeholder.
RENDER_VARS="SANDBOX_TOKEN BROWSER_TOKEN REDIS_PASSWORD FLAME_TAG FLAME_CONSUL_ADDR_HOST FLAME_CONSUL_ADDR_NET FLAME_REDIS_ADDR"

# render <template> <output> [mode] - substitute ${VAR} for the variables above.
#
# sed over a named list rather than envsubst: envsubst replaces every $NAME it can resolve, which would also eat
# the nginx-style $host / $http_upgrade in a template that ever grows one. A placeholder that survives is
# reported: an empty token is not a startup error, it is a 401 three weeks later.
render() {
    local src=$1 dst=$2 mode=${3:-0644} tmp v val
    tmp=$(mktemp)
    cp "$src" "$tmp"
    for v in $RENDER_VARS; do
        val=${!v:-}
        val=$(printf '%s' "$val" | sed -e 's/[\\/&|]/\\&/g')
        sed -i "s|\${${v}}|${val}|g" "$tmp"
    done
    if grep -q '\${' "$tmp"; then
        warn "unresolved placeholder in $dst: $(grep -o '\${[A-Za-z_]*}' "$tmp" | sort -u | tr '\n' ' ')"
    fi
    mkdir -p "$(dirname "$dst")"
    install -m "$mode" "$tmp" "$dst"
    rm -f "$tmp"
}

# check_render_vars <config dir> - fail if a *rendered* template uses an unknown placeholder.
#
# nexus.yaml is deliberately excluded: its ${VAR} placeholders are expanded by nexus from the environment at
# startup, not by render(), and it never passes through this script.
check_render_vars() {
    local dir=$1 used
    used=$(grep -oh '\${[A-Za-z_]*}' \
        "$dir"/sandboxd.yaml "$dir"/browserd.yaml "$dir"/sandboxg.yaml "$dir"/browserg.yaml \
        "$dir"/redis/redis.conf.tmpl 2>/dev/null | tr -d '${}' | sort -u)
    local unknown=""
    for v in $used; do
        case " $RENDER_VARS " in
            *" $v "*) ;;
            *) unknown="$unknown $v" ;;
        esac
    done
    [ -z "$unknown" ] || die "templates use placeholders render() does not set:$unknown (add them to RENDER_VARS)"
}
