# browserd

[中文](README.zh.md) | English

The standalone browser pool: one Chromium per live session, with its own capacity limits and idle reclamation. It is a
separate service **and a separate Go module** on purpose — a browser is 200-400 MB and carries login state, so it must
not share a lifetime or a release cadence with the code-execution sandbox (`../sandboxd`).

## How nexus reaches it

nexus talks to `/v1/browser` with the **same frame protocol** as sandboxd's `/v1/interactive` (open frame +
`ChanStdin` + kill), so it reuses `flame/tools/sandbox.InteractiveClient`: a different URL, not a different client.

In a multi-instance deployment that URL is **browserg** (`../browserg`, default `:27090`), not this service: the gateway
owns session stickiness and the per-tenant quota. A single-instance deployment may point nexus straight here
(`:27091`) — the protocol is identical either way.

- `argv` and `mounts` in the open frame are ignored — this service decides which driver and which Chromium to launch.
- `Spec.Dir` is honoured, but as a **workspace path relative to `workspace_root`**, not a sandbox-internal path.
- `GET /v1/capabilities` is the plain-HTTP snapshot nexus pulls (availability, engine, workers, queue, headed support).
- `?tenant=` on the channel is the per-tenant quota key; it is not an isolation boundary.

## Build

```bash
# in this directory, for the host (development)
go build ./...

# cross-compiled for the image, like sandboxd's binaries
GOOS=linux GOARCH=amd64 go build -o bin/browserd-linux ./cmd/browserd
GOOS=linux GOARCH=amd64 go build -o bin/browserhost-linux ./cmd/browserhost
docker build -t flame/browserd:latest .
```

`browserhost` is the in-container driver: it speaks one JSON object per line over stdin/stdout and launches Chromium on
the first operation. `browserclient` is the manual verification tool.

## Run

```bash
./browserd                                       # reads ./config.yaml
./browserd -config /etc/browserd/config.yaml     # any other path
```

`-config` defaults to `config.yaml` relative to the working directory; the image passes the absolute path because its
working directory is not `/etc/browserd`.

Three things must line up with the deployment, or the browser tool silently stops being usable:

| Setting | Must match |
|---|---|
| `workspace_root` | nexus's `storage.root_dir` — **the same directory**, not merely the same string (local dev: the same absolute path; containers: the same volume mount). Screenshots and downloads are written there, and the tenant reads them through nexus. A mismatch is silent by design of the filesystem, so startup logs an error when the root has no `tenants/` subdirectory. |
| `profile_root` | A writable volume of this service only. Profiles are private state and are deleted when a session ends. |
| `token` | nexus's `browser.token`. Empty means no authentication (internal network only). |

nexus side: `browser.url` (empty = this deployment has no browser) and `browser.token`.

**Registration**: `registry.enabled: true` announces this instance to consul/nacos after HTTP is up and keeps it alive, so
browserg discovers it on its next pull — starting a second instance needs no gateway restart. An unreachable registry does
not stop this service; it only makes the instance invisible to discovery, so list it in the gateway's static `backends`
too if that must not happen.

## Behaviour worth knowing

- **One session = one Chromium**, never reused across sessions — reuse would hand one tenant's cookies to the next. What
  is pooled is capacity: `workers`, `max_per_tenant`, queueing, and idle reclamation (`idle_timeout_sec` + `janitor_sec`).
- **An open frame without a spec never spawns.** Probes and kills go through that path, so stopping a run cannot launch
  a browser only to destroy it.
- A disconnect does not kill the browser (cookies and tabs survive); a kill frame does, and deletes the profile.

## Verify

```bash
go test ./...                  # unit tests: no browser installation needed
go vet ./...
```

The end-to-end tests need a running pool:

```bash
BROWSERD_TEST_URL=ws://127.0.0.1:27091/v1/browser BROWSERD_TEST_WORKSPACE=e2e go test ./internal/browserpool -run E2E -v
```

Or drive it by hand through the real protocol:

```bash
go run ./cmd/browserclient -session s1 -tenant t0 -workspace e2e \
  -op '{"id":1,"op":"open","url":"https://example.com"}'
```
