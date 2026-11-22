# syntax=docker/dockerfile:1.7
#
# Flame PoC — the product stack in one container: postgres, redis, nexus, sandboxd, browserd and nginx.
#
# The LLM gateway is *not* here: LiteLLM runs from its official image as a second container. `docker-compose.yml` in
# this directory is the entry point now — building this file alone gives a stack without a gateway, which nexus refuses
# to serve without (every model is provisioned through it).
#
# Build (context is the repository root, not this directory):
#   docker build -f deploy/poc/Dockerfile -t flame-poc .
#
# Why the context is the root: every module has `replace flame/tools => ../tools`, so a module directory alone cannot be
# compiled. `deploy/poc/Dockerfile.dockerignore` keeps the context small (no .git, no node_modules, no build output).

# ---------------------------------------------------------------------------
# 1) Go binaries (builder only — the runtime image has no toolchain)
# ---------------------------------------------------------------------------
FROM golang:1.25-bookworm AS gobuild

# The module proxy is an argument because it is the one thing that differs per network: on some networks
# proxy.golang.org refuses the connection outright and the failure reads as "the build is broken". Override it with
#   docker compose -f deploy/poc/docker-compose.yml build --build-arg GOPROXY=https://goproxy.cn,direct
# or put GOPROXY=... in deploy/poc/.env (docker-compose.yml passes the value through).
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=$GOPROXY

WORKDIR /src
COPY tools/     ./tools/
COPY nexus/     ./nexus/
COPY sandboxd/  ./sandboxd/
COPY browserd/  ./browserd/

# CGO off: every dependency is pure Go (pgx / chromedp / x-crypto), so a static binary is all we need.
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
RUN set -eux; \
    cd /src/nexus    && go build -trimpath -ldflags "-s -w" -o /out/nexus       ./cmd; \
    cd /src/nexus    && go build -trimpath -ldflags "-s -w" -o /out/seedpoc     ./cmd/seedpoc; \
    cd /src/sandboxd && go build -trimpath -ldflags "-s -w" -o /out/sandboxd    .; \
    cd /src/sandboxd && go build -trimpath -ldflags "-s -w" -o /out/codehost    ./cmd/codehost; \
    cd /src/browserd && go build -trimpath -ldflags "-s -w" -o /out/browserd    ./cmd/browserd; \
    cd /src/browserd && go build -trimpath -ldflags "-s -w" -o /out/browserhost ./cmd/browserhost

# ---------------------------------------------------------------------------
# 2) Desktop bundle (builder only)
# ---------------------------------------------------------------------------
FROM node:20-bookworm AS webbuild

WORKDIR /src/desktop
COPY desktop/package.json desktop/package-lock.json* ./
# `npm ci` needs the lockfile; fall back to install so a tree without one still builds.
RUN if [ -f package-lock.json ]; then npm ci; else npm install; fi
COPY desktop/ ./
RUN npm run build

# ---------------------------------------------------------------------------
# 3) Runtime: the product stack (the gateway is the compose file's second service)
# ---------------------------------------------------------------------------
# Debian 13 (trixie) rather than 12 (bookworm): python3 is 3.13 here, the same generation the sandbox contract in
# sandboxd/Dockerfile is written against (Debian 13 + python3.13 + lua5.4), so a script behaves the same in this box as
# in a docker-mode sandbox. bookworm hands out 3.11 and a different slice of the stdlib — 3.12 had already removed
# imp, and 3.13 removed cgi / telnetlib / pipes / crypt and a dozen more — which is exactly the "works on my box, fails
# in the sandbox" split this image exists to avoid.
#
# PostgreSQL comes from the PGDG repository instead of the distribution: Debian 13 ships 17, and this box is specified
# against 18. entrypoint.sh resolves the binary directory by glob, so nothing else has to know the version — but only
# 18 may be installed, or `ls -d /usr/lib/postgresql/*/bin | head -n1` would pick the older one.
# chromium / lua5.4 / gosu / supervisor keep their distribution package names.
FROM debian:trixie-slim

# Credentials for the PoC box. Override with -e on docker run; these values only have to be "not the default
# placeholder" — nexus refuses to start on an empty / CHANGE_ME JWT secret.
ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    JWT_SECRET="poc-only-jwt-secret-change-me-0123456789abcdef" \
    FLAME_VALUE_KEY="poc-only-value-key-change-me-0123456789abcdef" \
    LITELLM_MASTER_KEY="sk-poc-master"

# Python packages for agent scripts are installed here, not by the agent at runtime. Two reasons:
#   - a docker-mode sandbox is `--network none` with a read-only rootfs, so a script that pip-installs at runtime
#     simply fails there; keeping this image at "whatever the agent managed to install" would hide that until later;
#   - inside this container a runtime install lands in a writable system path and disappears with the next
#     `docker run`, which reads as "the script worked yesterday".
# The set matches the sandbox contract in sandboxd/Dockerfile: openpyxl / xlrd / pandas / numpy (the xlsx class of
# scripts), plus requests / python-docx / beautifulsoup4 (fetching, Word documents, HTML parsing). Those three are small
# and hit in almost every session; anything heavier (matplotlib, Pillow) is left to a venv the script builds itself —
# in this box that works, in a docker-mode sandbox it does not, and that difference should stay visible.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        redis-server \
        nginx \
        supervisor \
        gosu \
        curl ca-certificates \
        chromium \
        fonts-liberation fonts-noto-cjk \
        python3 python3-venv \
        python3-openpyxl python3-xlrd python3-pandas python3-numpy \
        python3-requests python3-docx python3-bs4 \
        lua5.4 \
        procps; \
    rm -rf /var/lib/apt/lists/*

# PostgreSQL 18 from PGDG. The distribution repository on trixie only carries 17, and the cluster in /data/pg is created
# by whatever major is installed — an existing volume from an older major has to be recreated (entrypoint.sh says so in
# one sentence instead of letting postgres fail on the data directory).
#
# `postgresql-18`, not `postgresql`: the meta package would drag in 17 as well, and the glob in the next step picks the
# lexicographically first directory, i.e. the wrong binary.
#
# The package's postinst also creates a cluster under /etc/postgresql and /var/lib/postgresql; this box only ever uses
# /data/pg, so that one is dropped rather than left behind as a second, unused major.
RUN set -eux; \
    install -d /usr/share/postgresql-common/pgdg; \
    curl -fsSL https://www.postgresql.org/media/keys/ACCC4CF8.asc \
        -o /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc; \
    printf '%s\n' \
        'Types: deb' \
        'URIs: https://apt.postgresql.org/pub/repos/apt' \
        'Suites: trixie-pgdg' \
        'Components: main' \
        'Signed-By: /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc' \
        > /etc/apt/sources.list.d/pgdg.sources; \
    apt-get update; \
    apt-get install -y --no-install-recommends postgresql-18 postgresql-client-18; \
    rm -rf /var/lib/apt/lists/*; \
    pg_dropcluster --stop 18 main || true

# Build-time self-check, same idea as sandboxd/Dockerfile: a missing interpreter or package has to fail the build, not
# the first script someone runs. The versions are also written to /etc/flame-versions so "what is actually in this
# box" is answerable with one `docker exec ... cat`, matching the docker-mode sandbox image.
RUN set -eux; \
    python3 --version; \
    /usr/bin/lua5.4 -v; \
    python3 -c 'import json, csv, zipfile, sys; print("stdlib ok")'; \
    python3 -c 'import openpyxl, xlrd, pandas, numpy, requests, docx, bs4; print("py-deps ok:", openpyxl.__version__, xlrd.__version__, pandas.__version__, numpy.__version__, requests.__version__, docx.__version__, bs4.__version__)'; \
    { echo "python: $(python3 --version 2>&1)"; \
      echo "libs:   $(python3 -c 'import openpyxl,xlrd,pandas,numpy,requests,docx,bs4;print("openpyxl",openpyxl.__version__,"xlrd",xlrd.__version__,"pandas",pandas.__version__,"numpy",numpy.__version__,"requests",requests.__version__,"docx",docx.__version__,"bs4",bs4.__version__)')"; \
      echo "lua:    $(/usr/bin/lua5.4 -v 2>&1)"; \
    } > /etc/flame-versions; \
    cat /etc/flame-versions

# There is no LiteLLM install in this image. The gateway runs from the official `litellm-database` image as a second
# container (see docker-compose.yml): that removed the single largest layer here (~400 MB) and, more importantly, the
# constraint that every gateway dependency needed a wheel for whatever Python this base image ships.
#
# In-container paths for the host-side binaries. postgres tools are version-suffixed (/usr/lib/postgresql/18/bin), and
# gosu lives in /usr/bin on some distributions and /usr/sbin on others, so both are resolved once here instead of being
# hardcoded in the configs.
#
# The version assertion at the end is the point of the glob: with two majors installed the older one sorts first and the
# cluster would silently be created by the wrong binary.
RUN set -eux; \
    pgbindir="$(ls -d /usr/lib/postgresql/*/bin | head -n1)"; \
    "$pgbindir/postgres" --version | grep -q ' 18\.'; \
    ln -sf "$pgbindir/postgres" /usr/local/bin/flame-pg; \
    ln -sf "$pgbindir/initdb"   /usr/local/bin/flame-initdb; \
    ln -sf "$pgbindir/pg_ctl"   /usr/local/bin/flame-pgctl; \
    ln -sf /usr/bin/lua5.4      /usr/local/bin/lua; \
    ln -sf "$(command -v gosu)" /usr/local/bin/flame-gosu

# ---- binaries and static content -------------------------------------------------
COPY --from=gobuild /out/ /opt/flame/bin/
COPY --from=webbuild /src/desktop/dist/ /usr/share/nginx/html/
COPY nexus/static/ /opt/flame/static/

# ---- configuration ---------------------------------------------------------------
# nexus has no -config flag: it reads ./config.yaml from its working directory, which is why its file is named
# config.yaml inside its own directory (see the [program:nexus] block in supervisord.conf).
COPY deploy/poc/config/nexus.yaml     /etc/flame/nexus/config.yaml
COPY deploy/poc/config/sandboxd.yaml  /etc/flame/sandboxd/config.yaml
COPY deploy/poc/config/browserd.yaml  /etc/flame/browserd/config.yaml
COPY deploy/poc/nginx/default.conf    /etc/nginx/conf.d/default.conf
COPY deploy/poc/supervisord.conf      /etc/supervisor/supervisord.conf
COPY deploy/poc/entrypoint.sh         /usr/local/bin/entrypoint.sh

# ---- platform SQL: copied from the repository, never duplicated -----------------
# The order is the dependency order used by scripts/verify-sql.ps1 (see the note there: alphabetical order does not work,
# the asset_* scripts need shared_assets first). The destination name also fixes the order entrypoint.sh applies them in,
# so every script under nexus/platform/sql/ needs a line here: scripts/verify-sql.ps1 fails when one is missing, because a
# migration that never reaches the image leaves an existing /data volume on the old schema while the new code runs on it.
COPY nexus/platform/sql/platform.sql                 /opt/flame/sql/10-platform.sql
COPY nexus/platform/sql/quota_storage.sql            /opt/flame/sql/20-quota_storage.sql
COPY nexus/platform/sql/flow_configs.sql             /opt/flame/sql/21-flow_configs.sql
COPY nexus/platform/sql/agent_configs.sql            /opt/flame/sql/22-agent_configs.sql
COPY nexus/platform/sql/llm_provider_presets.sql     /opt/flame/sql/23-llm_provider_presets.sql
COPY nexus/platform/sql/llm_presets_seed.sql         /opt/flame/sql/24-llm_presets_seed.sql
COPY nexus/platform/sql/zz_limit_defaults.sql        /opt/flame/sql/25-limit_defaults.sql
COPY nexus/platform/sql/flow_run_identity.sql        /opt/flame/sql/26-flow_run_identity.sql
COPY nexus/platform/sql/agent_run_identity.sql       /opt/flame/sql/27-agent_run_identity.sql
COPY nexus/platform/sql/agent_run_session_index.sql  /opt/flame/sql/28-agent_run_session_index.sql
COPY nexus/platform/sql/tenant_capabilities.sql      /opt/flame/sql/30-tenant_capabilities.sql
COPY nexus/platform/sql/capability_gates.sql         /opt/flame/sql/40-capability_gates.sql
COPY nexus/platform/sql/capability_code_rename.sql   /opt/flame/sql/50-capability_code_rename.sql
COPY nexus/platform/sql/shared_assets.sql            /opt/flame/sql/60-shared_assets.sql
COPY nexus/platform/sql/asset_listing_rename.sql     /opt/flame/sql/61-asset_listing_rename.sql
COPY nexus/platform/sql/asset_build_rename.sql       /opt/flame/sql/62-asset_build_rename.sql
COPY nexus/platform/sql/asset_pack_digest.sql        /opt/flame/sql/63-asset_pack_digest.sql
COPY nexus/platform/sql/listing_policy.sql           /opt/flame/sql/64-listing_policy.sql
COPY nexus/platform/sql/peer_market.sql              /opt/flame/sql/65-peer_market.sql
COPY nexus/platform/sql/plugin_gates.sql             /opt/flame/sql/66-plugin_gates.sql
COPY nexus/platform/sql/tenant_plugins.sql           /opt/flame/sql/67-tenant_plugins.sql
COPY nexus/platform/sql/customtool_capability_exec.sql /opt/flame/sql/68-customtool_capability_exec.sql
COPY deploy/poc/postgres/90-admin.sql                /opt/flame/sql/90-admin.sql

# The package's default site listens on :80 with root /var/www/html. This image has exactly one entry point, so it goes;
# `nginx -t` then validates the server block copied above.
RUN set -eux; \
    chmod 0755 /usr/local/bin/entrypoint.sh /opt/flame/bin/*; \
    mkdir -p /data /var/log/flame /var/run/postgresql /var/lib/flame/profiles; \
    chown -R postgres:postgres /var/run/postgresql; \
    rm -f /etc/nginx/sites-enabled/default; \
    nginx -t

EXPOSE 27001

# Real readiness: nginx up AND nexus ready (which itself waits for postgres, redis and litellm).
HEALTHCHECK --interval=10s --timeout=5s --start-period=180s --retries=12 \
    CMD curl -fsS http://127.0.0.1:27001/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["supervisord", "-n", "-c", "/etc/supervisor/supervisord.conf"]
