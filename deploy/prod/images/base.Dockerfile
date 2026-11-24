# syntax=docker/dockerfile:1
#
# flame/base: everything the nexus container needs that is not a Flame binary.
#
# It exists as its own image because of the layering argument: the Python runtime and its packages change
# once or twice a year, the binaries change every release. Rebuilding on top of an unchanged base adds one
# thin layer instead of re-running apt-get, which is what makes a release take seconds.
#
# Build: docker build -f images/base.Dockerfile -t flame/base:<tag> images
# (driven by scripts/images.sh - do not call it directly, the tag has to match FLAME_TAG).

FROM debian:trixie-slim

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    HOME=/tmp

# Python is needed for the custom-plugin runtime (`plugin.python: python3` in nexus.yaml) and for the workflow
# snippets the *agent* is handed by nexus; the sandbox itself runs in its own image.
#
# The package set mirrors sandboxd/Dockerfile on purpose: a script that works in the sandbox must work here,
# and the reverse. curl is the container health check.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        python3 \
        python3-openpyxl \
        python3-xlrd \
        python3-pandas \
        python3-numpy \
        python3-requests \
        python3-docx \
        python3-bs4; \
    rm -rf /var/lib/apt/lists/*

# Build-time self-check: a missing interpreter or package has to fail the build, not the first plugin call.
RUN set -eux; \
    python3 --version; \
    python3 -c 'import openpyxl, xlrd, pandas, numpy, requests, docx, bs4; print("py-deps ok")'; \
    { echo "python: $(python3 --version 2>&1)"; \
      echo "libs:   $(python3 -c 'import openpyxl,xlrd,pandas,numpy,requests,docx,bs4;print("openpyxl",openpyxl.__version__,"pandas",pandas.__version__)')"; \
    } > /etc/flame-versions; \
    cat /etc/flame-versions

# Paths the application image fills in. Created here so the application layer contains only files.
#
# The storage root is created as 1000:1000 because a named volume is initialised from the image: on a host with a
# bind mount install.sh sets the owner itself, but "docker compose up" with a named volume would otherwise hand
# nexus a root-owned directory and the first tenant write fails with EACCES.
RUN install -d /opt/flame/bin /opt/flame/static /var/lib/flame/nexus \
    && install -d -o 1000 -g 1000 /srv/flame/data

# Rendered at container start, so it must exist and be writable by uid 1000 (the uid the sandbox also uses).
#
# The log directories are created here with that owner for the same reason as the storage root: a named volume
# mounted on a path that exists in the image inherits its ownership, while one mounted on a path that does not
# exist is created root-owned and the service dies on "failed to open log file" during startup.
RUN install -d -o 1000 -g 1000 /var/lib/flame/nexus/logs \
    && install -d -o 1000 -g 1000 /var/log/flame/nexus /var/log/flame/sandboxg /var/log/flame/browserg

WORKDIR /var/lib/flame/nexus
USER 1000:1000
