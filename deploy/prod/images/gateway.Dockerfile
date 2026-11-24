# syntax=docker/dockerfile:1
#
# flame/gateway: sandboxg and browserg, one image.
#
# Both are pure-Go HTTP/websocket routers with no filesystem dependency of their own (the sticky bindings live
# in Redis, the backend list comes from Consul), so the image is `scratch` with two binaries in it. That is
# also the reason there is no shell and no health-check tool inside: compose runs the container with
# `user: 1000:1000` and the log directory is a host bind mount, and the readiness probe is the gateway's own
# /healthz, called from the outside.
#
# Deliberately no ENTRYPOINT: the same image serves two services, so the compose file names the binary
# (`command: ["/opt/flame/bin/sandboxg", "-config", ...]`). An entrypoint here would append those arguments to
# whichever binary it named.
#
# Build: docker build -f images/gateway.Dockerfile -t flame/gateway:<tag> .

FROM scratch

COPY --chmod=0755 out/bin/sandboxg /opt/flame/bin/sandboxg
COPY --chmod=0755 out/bin/browserg /opt/flame/bin/browserg

EXPOSE 27080
EXPOSE 27090
