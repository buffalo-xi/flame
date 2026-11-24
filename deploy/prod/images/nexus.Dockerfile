# syntax=docker/dockerfile:1
#
# flame/nexus: the platform API, the console's backend, and the two one-shot jobs that ship with it.
#
# The build context is deploy/prod, because everything it copies comes from out/ - the directory scripts/build.sh
# fills. Nothing is compiled here: the host has Go and node, the image gets the products.
#
# Build: docker build -f images/nexus.Dockerfile --build-arg FLAME_TAG=<tag> -t flame/nexus:<tag> .
#
# FLAME_BASE exists for the registry path: a CI build pushes flame/base first and then points this at the pushed
# name (ghcr.io/<org>/flame/base), so the image is built from the same base other machines will pull.

ARG FLAME_TAG=latest
ARG FLAME_BASE=flame/base
FROM ${FLAME_BASE}:${FLAME_TAG}

# nexus serves the console and the API; backfillversions is the version-directory repair tool; bootstrap is
# the one-shot seeder the `bootstrap` compose job runs.
#
# TODO(bootstrap): the binary is still compiled from cmd/seedpoc and installed under the production name. The
# contract is already the production one (same flags, same idempotence), so the rename to cmd/bootstrap is a
# naming change, not a behaviour change - and until it happens, keeping one implementation is better than two
# that drift.
# --chmod, not a later `RUN chmod`: the base image already switches to uid 1000, so a RUN step cannot change the
# mode of a root-owned file. It also makes the result independent of the build host's filesystem: a checkout on
# Windows produces 0666 files, and a binary without the execute bit fails as "exec format error"-adjacent noise.
COPY --chmod=0755 out/bin/nexus            /opt/flame/bin/nexus
COPY --chmod=0755 out/bin/bootstrap        /opt/flame/bin/bootstrap
COPY --chmod=0755 out/bin/backfillversions /opt/flame/bin/backfillversions

# Provider icons and other fixed assets, served as /static (server.static_root).
COPY out/static/ /opt/flame/static/

EXPOSE 27006

# Readiness, not liveness: /health answers 503 while a dependency is missing and names it in the body.
HEALTHCHECK --interval=15s --timeout=5s --start-period=90s --retries=8 \
    CMD curl -fsS http://127.0.0.1:27006/health >/dev/null || exit 1

# nexus has no -config flag: it loads ./config.yaml from the working directory set by the base image
# (/var/lib/flame/nexus), which is where compose mounts the rendered configuration.
ENTRYPOINT ["/opt/flame/bin/nexus"]
