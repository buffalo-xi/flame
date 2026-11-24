#!/usr/bin/env bash
# Build everything a production release needs, in one command.
#
#   ./scripts/release.sh --tag 2026.12.01
#   ./scripts/release.sh --tag 2026.12.01 --no-images    # products only (no docker needed)
#
# Products land in deploy/prod/release (gitignored):
#   flame-prod-<tag>.tar.gz   runnable tree: compose + config + images + scripts + systemd + env + out/
#   flame-images-<tag>.tar   the five images, for a machine without a registry
#   SHA256SUMS               sha256 of both archives
#
# Why one entry point: build.sh, images.sh build and images.sh save are three commands with an order, and the offline
# bundle is the thing that actually travels. A release that forgets the image tarball, or packs a tree without the
# binaries build.sh produced, fails only on the target host - after the copy.
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
prod=$(cd -- "$here/.." && pwd)
rel=$prod/release

log() { printf '\033[36m==> %s\033[0m\n' "$*"; }
die() { printf 'release.sh: %s\n' "$*" >&2; exit 1; }

tag=""
with_images=1
while [ $# -gt 0 ]; do
    case "$1" in
        --tag) tag=${2:?--tag needs a value}; shift 2 ;;
        --no-images) with_images=0; shift ;;
        -h|--help) sed -n '2,14p' "${BASH_SOURCE[0]}"; exit 0 ;;
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
    log "saving the image tarball"
    "$here/images.sh" save --tag "$tag" --dir "$rel"
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
cat <<EOF

on the target machine (copy both files into the same directory first):
  mkdir -p /srv/flame/dist && cd /srv/flame/dist
  tar -xzf flame-prod-$tag.tar.gz
  sudo ./scripts/install.sh --tag $tag --images /srv/flame/dist
EOF
if [ "$with_images" -eq 0 ]; then
    cat <<EOF

No image tarball in this bundle (--no-images): the target machine needs flame/{nexus,gateway,web,sandbox}:$tag
already in its docker daemon, otherwise drop --images from the install command.
EOF
fi
