#!/usr/bin/env bash
# Post-install acceptance, from the host, without entering any container.
#
#   ./scripts/health.sh
#
# Every check prints ok/warn/FAIL. FAIL is what the runbook's section 13 calls a blocked release; warn is
# "the product works, this deployment is missing something optional" (a host with no browser to drive). The nsjail
# side of the same class of problem is checked by `install.sh --check`, not here.
set -uo pipefail

entry=${FLAME_ENTRY:-http://127.0.0.1:27001}
consul=${FLAME_CONSUL_HEALTH:-http://127.0.0.1:8500}
failed=0

ok()   { printf '  \033[32mok\033[0m    %s\n' "$*"; }
bad()  { printf '  \033[31mFAIL\033[0m  %s\n' "$*"; failed=$((failed + 1)); }
note() { printf '  \033[33mwarn\033[0m  %s\n' "$*"; }

command -v curl >/dev/null || { echo "health.sh: curl is required" >&2; exit 1; }

echo "containers"
# `flame` (not a raw compose call) so the tier-3 file list is included.
if running=$(flame ps --services --status running 2>/dev/null); then
    n=$(printf '%s\n' "$running" | sed '/^$/d' | wc -l)
    expected="consul postgres redis litellm nexus sandboxg browserg web"
    for svc in $expected; do
        if printf '%s\n' "$running" | grep -qx "$svc"; then
            :
        else
            bad "$svc is not running"
        fi
    done
    [ "$n" -ge 8 ] && ok "$n services running"
else
    bad "docker daemon not reachable (or /etc/flame/flame.env is unreadable)"
fi

echo "entry point"
code=$(curl -s -o /tmp/flame-health.json -w '%{http_code}' "$entry/health" 2>/dev/null || echo 000)
if [ "$code" = "200" ]; then
    ok "$entry/health 200"
else
    bad "$entry/health -> $code"
    # The body names the failing dependency; that is the whole point of the endpoint.
    [ -s /tmp/flame-health.json ] && cat /tmp/flame-health.json >&2
fi

if curl -fsS -o /dev/null "$entry/" 2>/dev/null; then
    ok "the console bundle is served"
else
    bad "the console bundle is not served at $entry/"
fi

echo "registry"
for svc in sandboxd browserd; do
    body=$(curl -s "$consul/v1/health/service/$svc?passing=1" 2>/dev/null || echo '')
    # The reply is one array element per registered instance, each with Node + Service + Checks. `"Service":{` is the
    # entry key and appears once per element; the bare string `"Service"` also matches the instance's name field and
    # every check's ServiceID/ServiceName, so counting it reported one healthy instance as two.
    n=$(printf '%s' "$body" | grep -o '"Service":{' | wc -l)
    if [ "$n" -ge 1 ]; then
        ok "$svc: $n passing instance(s)"
    else
        bad "$svc: nothing passing in consul at $consul"
        note "check FLAME_CONSUL_ADDR_HOST and, on tier 2+, the firewall to 27081 / 27091"
    fi
done

echo "executors (host)"
for svc in sandboxd:27081 browserd:27091; do
    name=${svc%%:*}
    port=${svc##*:}
    if curl -fsS -o /dev/null "http://127.0.0.1:$port/healthz" 2>/dev/null; then
        ok "$name answers on 127.0.0.1:$port"
    else
        bad "$name does not answer on 127.0.0.1:$port"
        note "systemctl status flame-$name"
    fi
done

echo "browser"
# browserd starts without a browser on purpose (the honest state is "not installed", not "refuses to boot"), so
# /healthz being 200 says nothing about the browser tools. This is the only place the difference is visible from
# the host; `--selftest` only runs `<browser> --version`, it never launches anything.
if out=$(/opt/flame/current/bin/browserhost --selftest 2>&1); then
    ok "browserhost selftest: $out"
else
    note "browserd has no browser to drive - the browser tools stay unavailable (${out})"
fi

echo "storage"
if [ -d /srv/flame/data/tenants ]; then
    ok "/srv/flame/data/tenants exists"
    # tenants/<schema>/agents/<name>/AGENT.md is four levels down; maxdepth 2 never matched and the check then
    # warned "run the bootstrap job" on an installation that had already run it.
    if [ -n "$(find /srv/flame/data/tenants -maxdepth 6 -name AGENT.md -print -quit 2>/dev/null)" ]; then
        ok "a tenant's definition-domain files were materialised"
    else
        note "no tenant has an AGENT.md yet - run the bootstrap job"
    fi
else
    bad "/srv/flame/data/tenants is missing"
fi

if [ "$failed" -gt 0 ]; then
    printf '\n%d check(s) failed\n' "$failed" >&2
    exit 1
fi
echo
echo "all checks passed"
