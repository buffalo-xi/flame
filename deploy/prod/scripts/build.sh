#!/usr/bin/env bash
# Compile the release products into deploy/prod/out.
#
#   ./scripts/build.sh
#
# Running on the release host (Linux) is the normal case, but nothing here needs a Linux machine: the Go
# targets are cross-compiled with CGO off (every dependency is pure Go) and the desktop bundle is a Vite build.
#
# Products (all gitignored):
#   out/bin/{nexus,bootstrap,backfillversions,sandboxd,codehost,browserd,browserhost,sandboxg,browserg}
#   out/static/    nexus' fixed assets (served as /static)
#   out/web/       the built desktop bundle
#   out/sql/       a copy of nexus/platform/sql, the exact set scripts/migrate-apply.sh applies
#
# Also written outside out/: sandboxd/bin/codehost-linux, because sandboxd/Dockerfile COPYs the sandbox-side
# codehost from its own build context. Building it here keeps one build of that binary instead of a second
# definition inside an image.
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
prod=$(cd -- "$here/.." && pwd)
repo=$(cd -- "$prod/../.." && pwd)
out=${OUT_DIR:-$prod/out}
bin=$out/bin

log() { printf '\033[36m==> %s\033[0m\n' "$*"; }
die() { printf 'build.sh: %s\n' "$*" >&2; exit 1; }

command -v go >/dev/null || die "go is not on PATH"
command -v npm >/dev/null || die "npm is not on PATH (needed for the desktop bundle; use --no-web to skip)"

skip_web=0
for arg in "$@"; do
    case "$arg" in
        --no-web) skip_web=1 ;;
        -h|--help) sed -n '2,20p' "${BASH_SOURCE[0]}"; exit 0 ;;
        *) die "unknown argument: $arg" ;;
    esac
done

mkdir -p "$bin"

# Static binaries, always for linux/amd64: the target is a Linux host, whatever this machine is.
export CGO_ENABLED=0 GOOS=linux GOARCH=amd64

build_go() {
    local module=$1 pkg=$2 name=$3
    ( cd "$repo/$module" && go build -trimpath -ldflags '-s -w' -o "$bin/$name" "$pkg" ) \
        || die "go build failed: $module $pkg"
    printf '  %-16s %6s\n' "$name" "$(du -h "$bin/$name" | cut -f1)"
}

log "compiling (linux/amd64) into $bin"
build_go nexus     ./cmd                  nexus
# The production name of the seeder. One implementation: the binary is today's cmd/seedpoc (see
# images/nexus.Dockerfile for why the rename is pending).
build_go nexus     ./cmd/seedpoc          bootstrap
build_go nexus     ./cmd/backfillversions backfillversions
build_go sandboxd  .                      sandboxd
build_go sandboxd  ./cmd/codehost         codehost
build_go browserd  ./cmd/browserd         browserd
build_go browserd  ./cmd/browserhost      browserhost
build_go sandboxg  .                      sandboxg
build_go browserg  .                      browserg

log "placing codehost into the sandbox image's build context"
mkdir -p "$repo/sandboxd/bin"
# cp + chmod rather than install: this also has to run on a Windows build host (Git Bash), where `install` does
# not exist. The mode only matters inside the sandbox image, which Docker normalises anyway.
cp -f "$bin/codehost" "$repo/sandboxd/bin/codehost-linux"
chmod 0755 "$repo/sandboxd/bin/codehost-linux" 2>/dev/null || true

if [ "$skip_web" -eq 1 ]; then
    log "skipping the desktop bundle (--no-web)"
else
    log "building the desktop bundle"
    # Only install when there is nothing to reuse: a release host usually already has node_modules from the
    # previous release, and `npm ci` deletes and re-fetches it every time.
    if [ ! -d "$repo/desktop/node_modules" ]; then
        if [ -f "$repo/desktop/package-lock.json" ]; then
            npm --prefix "$repo/desktop" ci
        else
            npm --prefix "$repo/desktop" install
        fi
    fi
    npm --prefix "$repo/desktop" run build
    rm -rf "$out/web"
    cp -a "$repo/desktop/dist" "$out/web"
fi

log "collecting static assets and the platform SQL"
rm -rf "$out/static" "$out/sql"
cp -a "$repo/nexus/static" "$out/static"
cp -a "$repo/nexus/platform/sql" "$out/sql"

log "done: $out"
printf 'next: ./scripts/images.sh build --tag <FLAME_TAG>\n'
