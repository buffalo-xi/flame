# syntax=docker/dockerfile:1

# browserd service image: one Chromium per live session.
#
# Build (binaries are cross-compiled on the host, like sandboxd's image — this module replaces flame/tools with ../tools,
# so the build context of a Go build would have to be the repository root, which is not worth the coupling):
#
#   cd browserd
#   GOOS=linux GOARCH=amd64 go build -o bin/browserd-linux ./cmd/browserd
#   GOOS=linux GOARCH=amd64 go build -o bin/browserhost-linux ./cmd/browserhost
#   docker build -t flame/browserd:latest .
#
# Contract (read config.yaml before changing anything):
#   - workspace_root must be **the same mount** as nexus's storage.root_dir: screenshots and downloads are written there
#     and the tenant reads them through nexus afterwards;
#   - profile_root is this service's private state and must be a writable volume: it is deleted when a session ends, and
#     a profile that survives a restart only leaks a few hundred megabytes;
#   - the process runs as a non-root user; Chromium needs --no-sandbox inside the container, which browserhost passes by
#     default (the container is the isolation boundary);
#   - the config file is mounted, not baked in: the two directory roots are deployment-specific.

FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8

# chromium: the browser itself. fonts-noto-cjk: without CJK fonts every Chinese page renders as boxes, and the
# accessibility snapshot then carries the same boxes, so the model reads nothing useful.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        chromium \
        ca-certificates \
        fonts-noto-cjk \
    && rm -rf /var/lib/apt/lists/*

COPY bin/browserhost-linux /usr/local/bin/browserhost
COPY bin/browserd-linux /usr/local/bin/browserd

RUN mkdir -p /data/workspace /data/profiles && chown -R 1000:1000 /data

USER 1000:1000

# The config is mounted at /etc/browserd/config.yaml; only the two data roots are declared here.
VOLUME ["/data/workspace", "/data/profiles"]
EXPOSE 27091

ENTRYPOINT ["/usr/local/bin/browserd", "-config", "/etc/browserd/config.yaml"]
