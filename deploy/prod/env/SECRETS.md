# Secrets of a Flame deployment

English | [中文](SECRETS.zh.md)

Every secret lives in `/etc/flame/flame.env` (mode 0600, owned by root), which `install.sh` generates with
`openssl rand -hex 24` per value. Nothing is baked into an image, and no secret is written into a configuration
file that ships inside a release.

Hex rather than base64 is not a style choice: `POSTGRES_PASSWORD` is embedded in the LiteLLM `DATABASE_URL`,
and base64's `+`, `/` and `=` would need escaping to stay a valid URI.

## Impact and rotation

| Variable | What it protects | Rotating it |
|---|---|---|
| `FLAME_TAG` | not a secret; pins every image and release directory | this *is* the upgrade/rollback mechanism |
| `POSTGRES_PASSWORD` | the `flame` role, and through it both databases | `ALTER ROLE`, then update this file and recreate `nexus`, `migrate` and `litellm`. Changing it here does **not** change the existing role: initdb only runs on an empty data directory |
| `REDIS_PASSWORD` | gateways' sticky bindings, run ledger, quota counters | edit `redis.conf` too (`flame` wrapper reads this file, Redis does not), then restart redis, nexus and both gateways |
| `JWT_SECRET` | every session token | rotating it invalidates all sessions at once - acceptable, and every replica must share one value |
| `FLAME_VALUE_KEY` | `global_vars` rows stored with `kind=secret` | **rotating it makes every stored ciphertext unreadable.** There is no double-key transition in this release; treat it as write-once |
| `LITELLM_MASTER_KEY` | nexus → LiteLLM control plane | update this file and restart `nexus`; the gateway reads it from its own environment |
| `LITELLM_SALT_KEY` | provider keys stored in LiteLLM's database | **never rotate.** Every stored provider key becomes undecryptable, and the models still appear in the list while every call fails with an auth error |
| `SANDBOX_TOKEN` | nexus ↔ sandboxg ↔ sandboxd | both sides must change together; a mismatch shows up as 401 on every call, not as a startup error |
| `BROWSER_TOKEN` | nexus ↔ browserg ↔ browserd | same as `SANDBOX_TOKEN` |
| `FLAME_ADMIN_PASSWORD` | the seed accounts created by the bootstrap job | change it in the console after the first login; it is only read when the accounts are *created* |
| `FLAME_API_KEY` | the tenant's primary-user API key | rotate or delete the key in the console; the seed job only fills in a missing one |
| `CONSUL_HTTP_TOKEN` (in `sandboxd.env`) | Consul ACLs, when enabled | both the registering executors and the discovering gateways need it |

## What must be identical across replicas

`JWT_SECRET` and `FLAME_VALUE_KEY` are per-deployment, not per-machine: two nexus copies with different values
log users out at random and cannot decrypt each other's stored secrets. `SANDBOX_TOKEN` / `BROWSER_TOKEN` and
the database credentials are shared with the gateways and the jobs for the same reason.

## Backups

`/etc/flame` is the only place these values live. Back it up encrypted, and remember that losing it means
re-issuing every token and re-entering every provider key - not "restoring from the database".
