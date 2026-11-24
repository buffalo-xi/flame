#!/usr/bin/env bash
# Build, push, save and load the Flame images.
#
#   ./scripts/images.sh build --tag 2026.12.01                        # the five images, in the local daemon
#   ./scripts/images.sh push  --tag 2026.12.01 --registry ghcr.io/acme/flame   # ... and to that namespace
#   ./scripts/images.sh save  --tag 2026.12.01 --dir /srv/flame/dist  # offline bundle, upstream images included
#   ./scripts/images.sh load  --dir /srv/flame/dist
#
# Everything is tagged with the release tag, including the base image: base changes rarely, so rebuilding it is a
# cache hit, and one tag keeps "which base does this app image sit on" answerable without a second version line. The
# sandbox image is built from sandboxd/Dockerfile as it stands - a second copy of that definition would drift, and the
# sandbox image *is* the contract (interpreters, packages, codehost).
#
# Delivery is one of two paths, never both: `push` puts the images on a registry for `install.sh --registry`;
# `save`/`load` write a tarball that also carries the four upstream images compose needs, for a host with neither a
# registry nor a network.
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
prod=$(cd -- "$here/.." && pwd)
repo=$(cd -- "$prod/../.." && pwd)
out=$prod/out

# shellcheck source=scripts/lib.sh
. "$here/lib.sh"

usage() {
    sed -n '2,16p' "${BASH_SOURCE[0]}"
    exit "${1:-0}"
}

[ $# -ge 1 ] || usage 1
cmd=$1
shift

tag="${FLAME_TAG:-}"
# Where the images are addressed. `flame` is the default (a release built here); a registry delivery sets it, and
# install.sh --registry writes the same value into flame.env so compose asks for the same names.
ns="${FLAME_IMAGE_NS:-flame}"
dir=""
with_sandbox=1
with_upstream=1

while [ $# -gt 0 ]; do
    case "$1" in
        --tag) tag=${2:?--tag needs a value}; shift 2 ;;
        --dir) dir=${2:?--dir needs a value}; shift 2 ;;
        # Where the images are addressed. Same value install.sh --registry takes, so the two halves of a registry
        # delivery cannot disagree. A trailing slash is dropped: `ghcr.io/acme/flame/` and `.../flame` are one name.
        --registry) ns=${2:?--registry needs a value}; ns=${ns%/}; shift 2 ;;
        # The four upstream images are what compose pulls. They belong in an offline bundle and nowhere else: a
        # registry delivery lets the target host pull them from their own publishers.
        --no-upstream) with_upstream=0; shift ;;
        # The sandbox image is the big one (interpreters, packages) and only matters once a tool call runs, so a
        # trial can skip it. A deployment cannot: the first `sandbox.exec` would ask for an image nobody has.
        --no-sandbox) with_sandbox=0; shift ;;
        -h|--help) usage 0 ;;
        *) die "unknown argument: $1" ;;
    esac
done

# ensure_local <namespaced image> - make sure it exists locally, retagging from the default `flame/...` name when the
# build ran without --registry. "Build here, push there" is the normal order, and rebuilding only to change a tag would
# waste the same minutes for nothing. Returns non-zero when neither name is in the daemon.
ensure_local() {
    local img=$1 plain
    docker image inspect "$img" >/dev/null 2>&1 && return 0
    case $img in
        "$ns"/*) plain="flame/${img#"$ns"/}" ;;
        *) return 1 ;;
    esac
    docker image inspect "$plain" >/dev/null 2>&1 || return 1
    log "tagging $plain as $img"
    docker tag "$plain" "$img"
}

case "$cmd" in
    build)
        [ -n "$tag" ] || die "--tag is required (the same value as FLAME_TAG)"
        [ -d "$out/bin" ] || die "$out/bin is missing: run ./scripts/build.sh first"
        [ -d "$out/web" ] || log "warning: $out/web is missing, the web image will serve an empty directory"

        log "$ns/base:$tag"
        docker build -f "$prod/images/base.Dockerfile" -t "$ns/base:$tag" "$prod/images"

        # FLAME_BASE, not the default: this image is built on the base that was just built (or pulled), so the
        # Dockerfile is not silently rebuilt on top of a stale flame/base:latest.
        log "$ns/nexus:$tag"
        docker build -f "$prod/images/nexus.Dockerfile" \
            --build-arg "FLAME_TAG=$tag" --build-arg "FLAME_BASE=$ns/base" \
            -t "$ns/nexus:$tag" "$prod"

        log "$ns/gateway:$tag"
        docker build -f "$prod/images/gateway.Dockerfile" -t "$ns/gateway:$tag" "$prod"

        log "$ns/web:$tag"
        docker build -f "$prod/images/web.Dockerfile" -t "$ns/web:$tag" "$prod"

        if [ "$with_sandbox" -eq 1 ]; then
            log "$ns/sandbox:$tag"
            # The context is sandboxd/ because the Dockerfile COPYs bin/codehost-linux from it; build.sh put it
            # there. Building from the repository root instead would silently produce an image without codehost.
            [ -f "$repo/sandboxd/bin/codehost-linux" ] \
                || die "sandboxd/bin/codehost-linux is missing: run ./scripts/build.sh first"
            docker build -f "$repo/sandboxd/Dockerfile" -t "$ns/sandbox:$tag" "$repo/sandboxd"
        else
            log "$ns/sandbox:$tag skipped (--no-sandbox): no tool call can run until it is built"
        fi

        log "built: $(flame_images "$ns" "$tag" | tr '\n' ' ')"
        ;;

    push)
        [ -n "$tag" ] || die "--tag is required"
        # The base goes too: a machine that builds from source has to resolve FLAME_BASE against the same registry,
        # and a CI cache keyed on it stays warm. Log in first (docker login <registry>); a failure here is the push.
        list=$(flame_images "$ns" "$tag")
        [ "$with_sandbox" -eq 1 ] || list=$(printf '%s\n' "$list" | grep -v '/sandbox:')
        # shellcheck disable=SC2046
        for img in $list; do
            ensure_local "$img" \
                || die "$img is not in the local docker daemon (docker pull $img, or ./scripts/images.sh build --tag $tag)"
            log "pushing $img"
            docker push "$img"
        done
        ;;

    save)
        [ -n "$tag" ] || die "--tag is required"
        [ -n "$dir" ] || die "--dir is required (where the tarball goes)"
        # mkdir rather than install: the release host may be Windows (Git Bash), where `install` does not exist.
        mkdir -p "$dir"
        if [ "$with_upstream" -eq 1 ]; then
            list=$(required_images "$prod" "$ns" "$tag")
        else
            list=$(flame_runtime_images "$ns" "$tag")
        fi
        [ "$with_sandbox" -eq 1 ] || list=$(printf '%s\n' "$list" | grep -v '/sandbox:')

        # docker save writes nothing for an image that is not local, and the resulting tarball looks fine until the
        # target host starts a service: check first, name the missing image, say which pull fixes it.
        # shellcheck disable=SC2046
        for img in $list; do
            ensure_local "$img" \
                || die "$img is not in the local docker daemon (docker pull $img, or ./scripts/images.sh build --tag $tag)"
        done

        # One tarball, not eight: an offline bundle is copied to the target machine by hand.
        file="$dir/flame-images-$tag.tar"
        count=$(printf '%s\n' "$list" | grep -c . || true)
        log "saving $count images to $file"
        # shellcheck disable=SC2086
        docker save -o "$file" $list
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
