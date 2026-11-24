#!/usr/bin/env bash
# Build, save and load the four Flame images plus the sandbox runtime image.
#
#   ./scripts/images.sh build --tag 2026.12.01
#   ./scripts/images.sh save  --tag 2026.12.01 --dir /srv/flame/dist
#   ./scripts/images.sh load  --dir /srv/flame/dist
#
# Everything is tagged with the release tag, including flame/base: base changes rarely, so rebuilding it is a
# cache hit, and one tag keeps "which base does this app image sit on" answerable without a second version
# line. The sandbox image is built from sandboxd/Dockerfile as it stands - a second copy of that definition
# would drift, and the sandbox image *is* the contract (interpreters, packages, codehost).
#
# `save`/`load` are the offline delivery path: no registry, no network on the target machine.
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
prod=$(cd -- "$here/.." && pwd)
repo=$(cd -- "$prod/../.." && pwd)
out=$prod/out

log() { printf '\033[36m==> %s\033[0m\n' "$*"; }
die() { printf 'images.sh: %s\n' "$*" >&2; exit 1; }

usage() {
    sed -n '2,20p' "${BASH_SOURCE[0]}"
    exit "${1:-0}"
}

[ $# -ge 1 ] || usage 1
cmd=$1
shift

tag="${FLAME_TAG:-}"
dir=""
with_sandbox=1

while [ $# -gt 0 ]; do
    case "$1" in
        --tag) tag=${2:?--tag needs a value}; shift 2 ;;
        --dir) dir=${2:?--dir needs a value}; shift 2 ;;
        # The sandbox image is the big one (interpreters, packages) and only matters once a tool call runs, so a
        # trial can skip it. A deployment cannot: the first `sandbox.exec` would pull an image nobody pushed.
        --no-sandbox) with_sandbox=0; shift ;;
        -h|--help) usage 0 ;;
        *) die "unknown argument: $1" ;;
    esac
done

images() {
    printf '%s\n' \
        "flame/base:${tag}" \
        "flame/nexus:${tag}" \
        "flame/gateway:${tag}" \
        "flame/web:${tag}" \
        "flame/sandbox:${tag}"
}

case "$cmd" in
    build)
        [ -n "$tag" ] || die "--tag is required (the same value as FLAME_TAG)"
        [ -d "$out/bin" ] || die "$out/bin is missing: run ./scripts/build.sh first"
        [ -d "$out/web" ] || log "warning: $out/web is missing, the web image will serve an empty directory"

        log "flame/base:$tag"
        docker build -f "$prod/images/base.Dockerfile" -t "flame/base:$tag" "$prod/images"

        log "flame/nexus:$tag"
        docker build -f "$prod/images/nexus.Dockerfile" --build-arg "FLAME_TAG=$tag" \
            -t "flame/nexus:$tag" "$prod"

        log "flame/gateway:$tag"
        docker build -f "$prod/images/gateway.Dockerfile" -t "flame/gateway:$tag" "$prod"

        log "flame/web:$tag"
        docker build -f "$prod/images/web.Dockerfile" -t "flame/web:$tag" "$prod"

        if [ "$with_sandbox" -eq 1 ]; then
            log "flame/sandbox:$tag"
            # The context is sandboxd/ because the Dockerfile COPYs bin/codehost-linux from it; build.sh put it
            # there. Building from the repository root instead would silently produce an image without codehost.
            [ -f "$repo/sandboxd/bin/codehost-linux" ] \
                || die "sandboxd/bin/codehost-linux is missing: run ./scripts/build.sh first"
            docker build -f "$repo/sandboxd/Dockerfile" -t "flame/sandbox:$tag" "$repo/sandboxd"
        else
            log "flame/sandbox:$tag skipped (--no-sandbox): no tool call can run until it is built"
        fi

        log "built: $(images | tr '\n' ' ')"
        ;;

    save)
        [ -n "$tag" ] || die "--tag is required"
        [ -n "$dir" ] || die "--dir is required (where the tarball goes)"
        # mkdir rather than install: the release host may be Windows (Git Bash), where `install` does not exist.
        mkdir -p "$dir"
        # One tarball, not five: an offline bundle is copied to the target machine by hand.
        file="$dir/flame-images-$tag.tar"
        log "saving to $file"
        # shellcheck disable=SC2046
        docker save -o "$file" $(images)
        log "size: $(du -h "$file" | cut -f1)"
        printf 'on the target machine: ./scripts/images.sh load --dir %s\n' "$dir"
        ;;

    load)
        [ -n "$dir" ] || die "--dir is required (where the tarball is)"
        found=0
        for f in "$dir"/*.tar; do
            [ -e "$f" ] || continue
            found=1
            log "loading $f"
            docker load -i "$f"
        done
        [ "$found" -eq 1 ] || die "no *.tar in $dir"
        ;;

    *)
        usage 1
        ;;
esac
