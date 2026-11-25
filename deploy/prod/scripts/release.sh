#!/usr/bin/env bash
# Build everything a production release needs, in one command.
#
#   ./scripts/release.sh --tag v2026.12.01                 # products + images + tarballs
#   ./scripts/release.sh --tag v2026.12.01 --no-images     # products only (no docker needed)
#   ./scripts/release.sh --tag v2026.12.01 --no-upstream   # leave consul/postgres/redis/litellm out of the tarball
#
# Products land in deploy/prod/release (gitignored):
#   flame-prod-<tag>.tar.gz   runnable tree: compose + config + images + scripts + systemd + env + out/
#   flame-images-<tag>.tar    the four runtime images plus the four upstream ones, for a host without a registry
#   SHA256SUMS                sha256 of both archives
#
# Why one entry point: build.sh, images.sh build and images.sh save are three commands with an order, and the offline
# bundle is the thing that actually travels. A release that forgets the image tarball, or packs a tree without the
# binaries build.sh produced, fails only on the target host - after the copy.
#
# A host that can reach a registry does not need this tarball: `./scripts/images.sh push --tag <tag>` publishes the
# images and the target host installs with `install.sh --registry <namespace>`.
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
prod=$(cd -- "$here/.." && pwd)
rel=$prod/release

# shellcheck source=scripts/lib.sh
. "$here/lib.sh"

tag=""
with_images=1
with_upstream=1
while [ $# -gt 0 ]; do
    case "$1" in
        --tag) tag=${2:?--tag needs a value}; shift 2 ;;
        --no-images) with_images=0; shift ;;
        # The upstream images (consul, postgres, redis, litellm) are what a fully offline host needs. Leaving them out
        # keeps the tarball small when the target can pull them itself.
        --no-upstream) with_upstream=0; shift ;;
        -h|--help) sed -n '2,18p' "${BASH_SOURCE[0]}"; exit 0 ;;
        *) die "unknown argument: $1" ;;
    esac
done

[ -n "$tag" ] || die "--tag is required (the same value as FLAME_TAG)"
command -v go >/dev/null || die "go is not on PATH"
command -v npm >/dev/null || die "npm is not on PATH (needed for the desktop bundle)"
if [ "$with_images" -eq 1 ]; then
    command -v docker >/dev/null || die "docker is not on PATH (use --no-images to skip the images)"
fi

rm -rf "$rel"
mkdir -p "$rel"

log "compiling the products"
"$here/build.sh"

if [ "$with_images" -eq 1 ]; then
    log "building the images"
    "$here/images.sh" build --tag "$tag"

    # Pulling here rather than leaving it to images.sh save: that one refuses to write a tarball with a hole in it, and
    # a release that stops after twenty minutes of building to say "docker pull postgres:18" is a worse trade.
    if [ "$with_upstream" -eq 1 ]; then
        log "pulling the upstream images"
        for img in $(upstream_images "$prod"); do
            docker pull "$img"
        done
    fi

    log "saving the image tarball"
    if [ "$with_upstream" -eq 1 ]; then
        "$here/images.sh" save --tag "$tag" --dir "$rel"
    else
        "$here/images.sh" save --tag "$tag" --dir "$rel" --no-upstream
    fi
fi

# The modes travel inside the tarball, and the first thing the target machine runs is ./scripts/install.sh: a build
# host that is not Linux (Git Bash) hands a fresh file 0666, which extracts as a script nobody can execute. install.sh
# normalises its own copy anyway; this makes the bundle correct before it is copied.
find "$prod/scripts" -name '*.sh' -exec chmod 0755 {} + 2>/dev/null || true
chmod 0755 "$prod/scripts/flame" 2>/dev/null || true

# The runnable tree is what install.sh reads from a release directory, minus the two directories that are per-machine:
# release/ (itself) and .devrun/ (a local trial's rendered secrets).
log "packing the runnable tree"
tar -czf "$rel/flame-prod-$tag.tar.gz" -C "$prod" \
    --exclude=./release --exclude=./.devrun \
    README.md README.zh.md \
    compose.yaml compose.dev.yaml compose.multi-host.yaml \
    config images scripts systemd env out

# sha256sum is in Git Bash's coreutils; shasum covers the hosts that only have Perl.
log "writing checksums"
hasher=sha256sum
command -v sha256sum >/dev/null || hasher="shasum -a 256"
(
    cd "$rel" || exit 1
    : >SHA256SUMS
    for f in *.tar *.tar.gz; do
        [ -e "$f" ] || continue
        # Unquoted on purpose: the fallback is the two-word command `shasum -a 256`.
        # shellcheck disable=SC2086
        $hasher "$f" >>SHA256SUMS
    done
)

log "done: $rel"
ls -lh "$rel"
# install.sh loads the tarball itself when it is given --images, so there is no separate `images.sh load` step here: one
# command to place the release, one directory for the images to come from - and that directory is the one the runnable
# tree was extracted into, because the two archives travel side by side.
if [ "$with_images" -eq 1 ]; then
    cat <<EOF

on the target machine (copy both files into the same directory first):
  mkdir -p /srv/flame/dist && cd /srv/flame/dist
  tar -xzf flame-prod-$tag.tar.gz
  sudo ./scripts/install.sh --tag $tag --images /srv/flame/dist

with a registry instead of the image tarball:
  ./scripts/images.sh push --tag $tag          # then on the target host:
  sudo ./scripts/install.sh --tag $tag --registry $FLAME_IMAGE_NS
EOF
else
    # The CI path: the tarball is only the host half, and the images come from wherever they were pushed.
    cat <<EOF

on the target machine (this bundle has no images: they come from a registry, or are already in the docker daemon):
  tar -xzf flame-prod-$tag.tar.gz
  sudo ./scripts/install.sh --tag $tag --registry $FLAME_IMAGE_NS
EOF
fi
if [ "$with_images" -eq 1 ] && [ "$with_upstream" -eq 0 ]; then
    cat <<EOF

The tarball has no upstream images (--no-upstream): the target host pulls consul, postgres, redis and litellm itself.
EOF
fi
