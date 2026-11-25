#!/usr/bin/env bash
# Build or verify the nsjail rootfs for one image generation.
#
#   ./scripts/rootfs.sh --tag v2026.11.25                    # export -> /opt/flame/rootfs/v2026.11.25
#   ./scripts/rootfs.sh --tag v2026.11.25 --check            # verify an existing one, changes nothing
#   ./scripts/rootfs.sh --tag v2026.11.25 --dir /opt/flame/rootfs --image ghcr.io/acme/flame/sandbox:<tag>
#
# Only `mode: nsjail` needs this; docker mode mounts the image directly. The image is the contract - interpreters,
# packages, codehost - so the tree is a `docker export` of flame/sandbox:<tag>, never hand-built, and it is versioned
# by tag because a stale rootfs is invisible: nsjail would keep running last generation's interpreters while docker
# mode runs the new ones.
#
# `docker export` carries no device nodes, and codehost opens /dev/null for every child, so the five nodes are
# created here. Where mknod is not permitted (a dev box without root, Docker Desktop's overlay) pass --no-dev-nodes -
# to the build and to --check, which then expects no nodes - and put `extra_args: ["-R", "/dev"]` in sandboxd.yaml
# instead: binding the host /dev read-only into the jail supplies the same devices without root.
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)

# shellcheck source=scripts/lib.sh
. "$here/lib.sh"

usage() {
    sed -n '2,16p' "${BASH_SOURCE[0]}"
    exit "${1:-0}"
}

tag=""
ns=""
image=""
dir=""
check_only=0
dev_nodes=1

while [ $# -gt 0 ]; do
    case "$1" in
        --tag) tag=${2:?--tag needs a value}; shift 2 ;;
        --image) image=${2:?--image needs a value}; shift 2 ;;
        --dir) dir=${2:?--dir needs a value}; shift 2 ;;
        --check) check_only=1; shift ;;
        --no-dev-nodes) dev_nodes=0; shift ;;
        -h|--help) usage 0 ;;
        *) die "unknown argument: $1" ;;
    esac
done

# The host's env file is the default source of the tag and the namespace, so nobody repeats what the machine already
# knows. It is not required: a build machine may export the two variables, and --tag overrides both.
if [ -f /etc/flame/flame.env ]; then
    # shellcheck disable=SC1091
    . /etc/flame/flame.env
fi

tag=${tag:-${FLAME_TAG:-}}
ns=${ns:-${FLAME_IMAGE_NS:-flame}}
dir=${dir:-/opt/flame/rootfs}

[ -n "$tag" ] || die "--tag is required (or FLAME_TAG in /etc/flame/flame.env)"
target=$dir/$tag
[ -n "$image" ] || image=$ns/sandbox:$tag

ok()   { printf '  \033[32mok\033[0m    %s\n' "$*"; }
bad()  { printf '  \033[31mFAIL\033[0m  %s\n' "$*"; failed=$((failed + 1)); }
note() { printf '  \033[33mwarn\033[0m  %s\n' "$*"; }
failed=0

# The lua link in the image is absolute (/usr/local/bin/lua -> /usr/bin/lua5.4). Testing it with -e would follow the
# link on the *host*, where that path means something else, so the target is resolved against the rootfs by hand.
lua_target() {
    # Separate declarations on purpose: in `local root=$1 link=$root/...` the right-hand sides are expanded
    # before the assignment, so `$root` would still be unset and `set -u` aborts the script.
    local root=$1
    local link=$root/usr/local/bin/lua
    local ref
    [ -L "$link" ] || return 1
    ref=$(readlink "$link")
    case "$ref" in
        /*) printf '%s\n' "$root$ref" ;;
        *)  printf '%s\n' "$(dirname "$link")/$ref" ;;
    esac
}

# verify <rootfs> - everything a jail needs to answer the first request. Read-only; used by --check and at the end of
# an export, so that a broken interpreter path is caught here instead of as "unavailable" on every call.
verify() {
    local t=$1 n missing=""

    [ -d "$t" ] && ok "rootfs $t" || { bad "rootfs $t missing"; return 0; }
    [ -x "$t/usr/local/bin/codehost" ] && ok "codehost in the rootfs" \
        || bad "no executable /usr/local/bin/codehost in $t (the export is incomplete)"
    [ -e "$t/usr/bin/python3" ] && ok "python3 in the rootfs" || bad "no /usr/bin/python3 in $t"
    [ -e "$t/usr/local/bin/node" ] && ok "node in the rootfs" \
        || note "no /usr/local/bin/node in $t (js tools will be unavailable)"

    if [ -L "$t/usr/local/bin/lua" ]; then
        if [ -e "$(lua_target "$t")" ]; then
            ok "lua link resolves inside the rootfs"
        else
            bad "/usr/local/bin/lua points at $(readlink "$t/usr/local/bin/lua"), which is not in $t (lua exits 255 at startup)"
        fi
    elif [ -e "$t/usr/bin/lua5.4" ]; then
        note "lua5.4 present but /usr/local/bin/lua is not a link (the image's contract is the link)"
    else
        bad "no lua in $t"
    fi

    if [ "$dev_nodes" -eq 0 ]; then
        note "device nodes skipped: sandboxd.yaml needs extra_args: [\"-R\", \"/dev\"]"
        return 0
    fi
    for n in null zero random urandom tty; do
        [ -c "$t/dev/$n" ] || missing="$missing /dev/$n"
    done
    if [ -z "$missing" ]; then
        ok "device nodes present"
    else
        bad "missing device node(s):$missing (codehost opens /dev/null for every fork)"
    fi
}

if [ "$check_only" -eq 1 ]; then
    log "checking $target"
    verify "$target"
    if command -v nsjail >/dev/null; then
        # The real test, and the only one that catches a link whose target went missing: run it.
        if nsjail -Mo --chroot "$target" -- /usr/bin/python3 -V >/dev/null 2>&1; then
            ok "nsjail -Mo --chroot $target -- /usr/bin/python3 -V"
        else
            bad "nsjail cannot run python3 inside $target (run that command by hand for the reason)"
        fi
    else
        note "nsjail absent: the chroot was not exercised (install it setuid root, then re-run --check)"
    fi
    [ "$failed" -eq 0 ] || die "$failed check(s) failed"
    log "rootfs ok (nothing was changed)"
    exit 0
fi

# --- build -----------------------------------------------------------------------------------------------------------------

[ "$(id -u)" -eq 0 ] || die "run as root (the tree is written under $dir, and mknod needs it)"
command -v docker >/dev/null || die "docker is not installed (the rootfs is a docker export)"
if [ -d "$target" ] && [ -n "$(ls -A "$target" 2>/dev/null)" ]; then
    die "$target already exists and is not empty - one directory per generation, so pass a new --tag (a rollback needs the old tree)"
fi

if ! docker image inspect "$image" >/dev/null 2>&1; then
    log "pulling $image"
    docker pull "$image" || die "cannot pull $image - on a host without a registry, load the bundle first (./scripts/images.sh load --dir <dir>)"
fi

cname=flame-rootfs-$$
cleanup() { docker rm -f "$cname" >/dev/null 2>&1 || true; }
trap cleanup EXIT

log "exporting $image"
docker create --name "$cname" "$image" true >/dev/null
install -d "$target"
docker export "$cname" | tar -x -C "$target"
cleanup
trap - EXIT

if [ "$dev_nodes" -eq 1 ]; then
    log "creating device nodes"
    install -d "$target/dev"
    # mknod is the one step a container cannot do for us, and the one that fails without root or on an overlay.
    if mknod -m 666 "$target/dev/null" c 1 3 2>/dev/null; then
        mknod -m 666 "$target/dev/zero"    c 1 5
        mknod -m 666 "$target/dev/random"  c 1 8
        mknod -m 666 "$target/dev/urandom" c 1 9
        mknod -m 666 "$target/dev/tty"     c 5 0
    else
        note "mknod failed (no permission on this filesystem)"
        note "the tree is still valid: put  extra_args: [\"-R\", \"/dev\"]  in the nsjail block of sandboxd.yaml"
        note "on a production host, re-run this script as root instead"
        dev_nodes=0
    fi
fi

log "verifying $target"
verify "$target"

if command -v nsjail >/dev/null; then
    log "exercising the chroot"
    if nsjail -Mo --chroot "$target" -- /usr/bin/python3 -V >/dev/null 2>&1; then
        ok "nsjail can chroot into it"
    else
        bad "nsjail cannot run python3 inside $target (run that command by hand for the reason)"
    fi
fi

[ "$failed" -eq 0 ] || die "$failed check(s) failed - the tree is at $target (fix it, or delete it and export again)"

cat <<EOF

Rootfs ready: $target

Next, in /etc/flame/sandboxd.yaml:
  mode: nsjail
  nsjail.rootfs: $target      # the template already defaults to /opt/flame/rootfs/\${FLAME_TAG}

then:  systemctl restart flame-sandboxd && ./scripts/health.sh
EOF
