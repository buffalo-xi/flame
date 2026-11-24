#!/usr/bin/env bash
# One end-to-end call, from the outside: log in, point a model at a provider, make it the default, expose an
# endpoint, call it with an API key, and check that the text we sent came back in the answer.
#
#   FLAME_LLM_BASE_URL=https://api.openai.com/v1 \
#   FLAME_LLM_API_KEY=sk-... \
#   FLAME_LLM_MODEL=gpt-4o-mini ./scripts/smoke.sh
#
# Optional: --entry http://host:27001 (default 127.0.0.1:27001), --cleanup (delete what it created).
#
# Why this exists next to health.sh: health.sh proves the processes are alive, and a stack can be perfectly healthy
# and still unable to answer, because "usable" also needs a default model, a released agent and an exposed endpoint.
# This script walks that path once and asserts on the answer, which is the only check a user cares about.
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=scripts/lib.sh
. "$here/lib.sh"

entry=${FLAME_ENTRY:-http://127.0.0.1:27001}
schema=${FLAME_TENANT_SCHEMA:-t0}
password=${FLAME_ADMIN_PASSWORD:-12345678}
# `primary` is the tenant administrator the seed creates; `user01` is a *platform* account, not a tenant one.
username=${FLAME_TENANT_USER:-primary}
cleanup=0

for arg in "$@"; do
    case "$arg" in
        --cleanup) cleanup=1 ;;
        -h|--help) sed -n '2,/^$/p' "${BASH_SOURCE[0]}"; exit 0 ;;
        --entry)   : ;;
        http://*|https://*) entry=$arg ;;
        *)         die "unknown argument: $arg" ;;
    esac
done

command -v curl >/dev/null || die "curl is required"
command -v python3 >/dev/null || die "python3 is required (to read the JSON responses)"

# ---- the model this run uses -------------------------------------------------
: "${FLAME_LLM_BASE_URL:?set FLAME_LLM_BASE_URL (include the /v1 suffix, e.g. https://api.openai.com/v1)}"
: "${FLAME_LLM_API_KEY:?set FLAME_LLM_API_KEY}"
# No apostrophe in these messages: inside ${VAR:?...} a single quote starts a quoted section and swallows the
# rest of the file.
: "${FLAME_LLM_MODEL:?set FLAME_LLM_MODEL (the provider model id, e.g. gpt-4o-mini)}"
provider_url=$FLAME_LLM_BASE_URL
provider_key=$FLAME_LLM_API_KEY
model_name=$FLAME_LLM_MODEL

stamp=$(date +%H%M%S)
nonce="flame-smoke-$stamp"
auth=""

# ---- helpers -----------------------------------------------------------------
jget() { # jget <json> <a.b.c>
    printf '%s' "$1" | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    print(""); raise SystemExit(0)
for key in sys.argv[1].split("."):
    if d is None:
        break
    if isinstance(d, list):
        d = d[int(key)] if key.isdigit() and int(key) < len(d) else None
    else:
        d = d.get(key)
print("" if d is None else d)
' "$2"
}

# The console API lives behind /nexus/ on the web tier (nginx strips the prefix, so nexus sees /admin/...).
# The tenant service API does not: /service/... is passed through as-is.
api_base="$entry/nexus"

api() { # api METHOD PATH [JSON]
    local method=$1 path=$2 body=${3:-}
    local args=(-sS -X "$method" "$api_base$path" -H "Accept: application/json")
    [ -n "$auth" ] && args+=(-H "Authorization: Bearer $auth")
    [ -n "$body" ] && args+=(-H "Content-Type: application/json" -d "$body")
    curl "${args[@]}" 2>/dev/null || printf '{"code":0,"msg":"connection failed"}'
}

ok() { # ok <json> <what>
    local code
    code=$(jget "$1" code)
    if [ "$code" != "200" ]; then
        printf '\n%s failed.\n  response: %s\n' "$2" "$(printf '%s' "$1" | head -c 600)" >&2
        exit 1
    fi
}

say() { printf '\033[36m==> %s\033[0m\n' "$*"; }

# ---- 1. log in as the tenant administrator -----------------------------------
say "login (tenant $schema)"
auth=""
resp=$(api POST /admin/login "{\"username\":\"$username\",\"password\":\"$password\",\"schema\":\"$schema\"}")
ok "$resp" "login"
auth=$(jget "$resp" data.token)
[ -n "$auth" ] || die "login returned no token"

# ---- 2. a provider and a default model ---------------------------------------
say "provider -> model -> provision (LiteLLM)"

# Find-or-create, not create: running this twice must not trip the unique keys (a model name is unique per
# tenant, and a provider name likewise). The names are fixed so the second run reuses the first run's rows.
find_id() { # find_id <field> <value>   (JSON arrives on stdin)
    python3 -c '
import json, sys
d = json.load(sys.stdin).get("data") or {}
items = d.get("items") if isinstance(d, dict) else d
for it in items or []:
    if str(it.get(sys.argv[1])) == sys.argv[2]:
        print(it.get("id")); break
else:
    print("")
' "$1" "$2"
}

provider_id=$(printf '%s' "$(api POST /admin/llmproviders/ '{"page":1,"limit":100}')" | find_id name smoke)
if [ -z "$provider_id" ]; then
    resp=$(api POST /admin/llmproviders/create "{
  \"name\":\"smoke\",\"url\":\"$provider_url\",\"protocol\":\"openai\",
  \"api_key\":\"$provider_key\",\"icon\":\"\",\"enabled\":true,\"extra_config\":{}}")
    ok "$resp" "creating the provider"
    provider_id=$(jget "$resp" data.id)
else
    warn "reusing the provider 'smoke' (delete it in the console to point at a different endpoint)"
fi

model_id=$(printf '%s' "$(api POST /admin/llmmodels/ '{"page":1,"limit":100}')" | find_id name "$model_name")
if [ -z "$model_id" ]; then
    resp=$(api POST /admin/llmmodels/create "{
  \"name\":\"$model_name\",\"provider_id\":\"$provider_id\",\"gateway_model\":\"smoke\",
  \"context_window\":32768,\"is_default\":true,
  \"base_params\":{\"max_tokens\":1024,\"temperature\":0},\"extra_params\":{},\"capabilities\":{}}")
    ok "$resp" "creating the model"
    model_id=$(jget "$resp" data.id)
else
    # The default model is what a production call uses; a reused non-default row would fail later with
    # llm.default_model_not_enabled.
    api PUT "/admin/llmmodels/$model_id" "{\"is_default\":true}" >/dev/null
fi

resp=$(api POST "/admin/llmmodels/$model_id/provision")
ok "$resp" "provisioning the model into the gateway"
gateway_id=$(jget "$resp" data.gateway_id)
[ -n "$gateway_id" ] || die "provision returned no gateway id (LiteLLM refused the model, see the response above)"

# ---- 3. an agent, a released version, an exposed endpoint ---------------------
say "agent -> version -> endpoint"
# `main` is the preinstalled supervisor agent; its id is fixed by the tenant template, but look it up anyway so
# this keeps working if the seed ever changes.
resp=$(api POST /admin/agents/ '{"page":1,"limit":50}')
ok "$resp" "listing agents"
agent_id=$(printf '%s' "$resp" | python3 -c '
import json, sys
d = json.load(sys.stdin).get("data") or {}
for it in d.get("items") or []:
    if it.get("name") == "main":
        print(it.get("id")); break
else:
    print("")
')
[ -n "$agent_id" ] || die "no agent named 'main' in tenant $schema (was the bootstrap job run?)"

# Publishing is skipped for the supervisor on purpose: `main` is served from the draft so that editing it takes
# effect immediately. Non-supervisor agents need a version, which is what the bootstrap job's backfill creates.
resp=$(api POST "/admin/agents/$agent_id/versions" '{"note":"smoke test"}')
# 200 or "already has this version" both leave the agent runnable; only a hard failure matters.

# An endpoint name must equal its target's name, so the endpoint is "main" and repeats re-use it rather than
# fail on the (name, protocol) unique key.
endpoint_name=main
resp=$(api POST /admin/svcendpoints/create "{
  \"name\":\"$endpoint_name\",\"description\":\"smoke test\",\"target_type\":\"agent\",
  \"target_id\":\"$agent_id\",\"protocol\":\"rpc\",\"input_schema\":{}}")
endpoint_id=$(jget "$resp" data.id)
if [ -z "$endpoint_id" ]; then
    endpoint_id=$(printf '%s' "$(api POST /admin/svcendpoints/ '{"page":1,"limit":100}')" | python3 -c '
import json, sys
d = json.load(sys.stdin).get("data") or {}
for it in d.get("items") or []:
    if it.get("name") == "main":
        print(it.get("id")); break
else:
    print("")
')
fi
[ -n "$endpoint_id" ] || die "could not create or find the endpoint: $(printf '%s' "$resp" | head -c 400)"

resp=$(api PUT "/admin/svcendpoints/$endpoint_id" '{"exposed":true}')
ok "$resp" "exposing the endpoint"

# ---- 4. an API key that may actually call it ---------------------------------
say "API key"
# The RPC surface has to accept API-key auth; a fresh tenant has this off, and then every key answers 401.
resp=$(api PUT /admin/rpcserverconfig '{"auth_type":"apikey","is_active":true,"auth_config":{}}')
[ "$(jget "$resp" code)" = "200" ] || warn "could not enable API-key auth on the RPC surface: $(jget "$resp" msg)"

# The binding this run created; empty when FLAME_API_KEY supplied an existing key, which belongs to the tenant and
# is never touched here. --cleanup revokes it, and a retry revokes it before issuing the next one, so repeated runs
# do not pile up credentials.
created_binding_id=""

# Creates a binding for the tenant's first user, setting $apikey and $created_binding_id.
#
# It must not be called as `apikey=$(create_apikey)`: a command substitution runs in a subshell, so the binding id
# assigned inside would be lost there and the key could never be revoked.
create_apikey() {
    local users uid
    users=$(api GET /admin/users)
    uid=$(printf '%s' "$users" | python3 -c '
import json, sys
d = json.load(sys.stdin).get("data")
items = d.get("items") if isinstance(d, dict) else d
print((items or [{}])[0].get("id", ""))
')
    [ -n "$uid" ] || die "cannot list the tenant's users"
    local r
    r=$(api POST /admin/svcauthbindings/create "{\"auth_type\":\"apikey\",\"user_id\":\"$uid\"}")
    ok "$r" "creating an API key"
    apikey=$(jget "$r" data.external_subject)
    created_binding_id=$(jget "$r" data.id)
}

# Best effort: a refused revoke must not turn the run red, and a key minted for this run is worthless afterwards.
drop_created_key() {
    [ -n "$created_binding_id" ] || return 0
    api DELETE "/admin/svcauthbindings/$created_binding_id" >/dev/null
    created_binding_id=""
}

# Deletes a row and says why when the console refuses it. Discarding the response made --cleanup announce success
# while every DELETE was answering 400 (an exposed endpoint, a model an agent still points at).
delete_or_warn() { # delete_or_warn <path> <what>
    local r
    r=$(api DELETE "$1")
    [ "$(jget "$r" code)" = "200" ] || warn "could not delete $2: $(jget "$r" msg)"
}

apikey=${FLAME_API_KEY:-}
[ -n "$apikey" ] || create_apikey

# ---- 5. call it --------------------------------------------------------------
say "POST /service/rpc/$schema as the tenant"
# $auth (the console JWT) stays set on purpose: the cleanup at the end deletes through api(), and emptying it here
# made every one of those DELETEs answer 401 - silently, because their output is discarded.
payload=$(python3 -c '
import json, sys
print(json.dumps({
    "jsonrpc": "2.0", "id": 1, "method": "tools/call",
    "params": {"name": sys.argv[1], "arguments": {"input": sys.argv[2]}},
}))' "main" "$nonce")

rpc() {
    curl -sS -o /tmp/flame-smoke.json -w '%{http_code}' -X POST "$entry/service/rpc/$schema" \
        -H "Authorization: Bearer $1" -H "Content-Type: application/json" -d "$payload" 2>/dev/null || echo 000
}

http_code=$(rpc "$apikey")
resp=$(cat /tmp/flame-smoke.json 2>/dev/null || echo '{}')
rm -f /tmp/flame-smoke.json

# The seeded key of a fresh install may not be a binding yet (and a tenant that has never had one needs one). One
# retry with a key the server just issued distinguishes "wrong key" from "this path is broken".
#
# Only 401/403 means the credential is at fault. Retrying every non-200 used to mint a second key on each run against
# a stack that was merely not ready yet: a 503 is readiness (nexus refuses all requests until its sandbox snapshot
# is in) and says nothing about the key.
if [ "$http_code" = "401" ] || [ "$http_code" = "403" ]; then
    warn "the credential was rejected ($http_code); issuing a new one"
    drop_created_key
    create_apikey
    http_code=$(rpc "$apikey")
    resp=$(cat /tmp/flame-smoke.json 2>/dev/null || echo '{}')
    rm -f /tmp/flame-smoke.json
fi

if ! printf '%s' "$resp" | grep -q "$nonce"; then
    cat >&2 <<EOF

The call did not come back with the text that was sent, so this installation cannot answer yet.

  sent:     $nonce
  response: $(printf '%s' "$resp" | head -c 800)

Most likely, in order of frequency:
  * no released version for the agent   -> run the bootstrap job (it backfills v1), or publish in the console
  * endpoint not exposed               -> the console's endpoint list shows it as retracted
  * API key not allowed / not active   -> the key must exist in the access keys of an active user
  * no default model, or not provisioned -> check the model list; provision it again after editing the provider
EOF
    # A failed run leaves nothing behind: the key it minted was for this run only, and exiting here would otherwise
    # skip the cleanup section entirely and keep it forever.
    drop_created_key
    exit 1
fi

echo
printf '\033[32manswer received\033[0m via /service/rpc/%s:\n\n' "$schema"
printf '%s' "$resp" | python3 -c '
import json, sys
d = json.load(sys.stdin)
res = d.get("result") or {}
texts = [c.get("text", "") for c in (res.get("content") or []) if isinstance(c, dict)]
print("\n".join(texts) if texts else json.dumps(res, ensure_ascii=False)[:800])
'

# ---- 6. tidy up (optional) ---------------------------------------------------
if [ "$cleanup" -eq 1 ]; then
    # An exposed endpoint refuses deletion (expose / retract are different actions), so retract it first.
    api PUT "/admin/svcendpoints/$endpoint_id" '{"exposed":false}' >/dev/null
    delete_or_warn "/admin/svcendpoints/$endpoint_id" "the smoke endpoint"
    delete_or_warn "/admin/llmmodels/$model_id" "the smoke model"
    delete_or_warn "/admin/llmproviders/$provider_id" "the smoke provider"
    drop_created_key
    say "cleanup finished: anything the console refused is reported above"
else
    say "kept them: provider 'smoke', model '$model_name' (gateway name 'smoke'), endpoint 'main'"
fi
