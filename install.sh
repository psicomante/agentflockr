#!/bin/sh
# Install the latest agentflockr release:
#   curl -fsSL https://raw.githubusercontent.com/psicomante/agentflockr/main/install.sh | sh
# AGENTFLOCKR_VERSION picks a release tag (default: latest) and
# AGENTFLOCKR_INSTALL_DIR the destination (default: ~/.local/bin).
#
# Everything runs inside main, called on the last line. Piped into sh, the
# script runs as it arrives; if the download stops partway, sh has only read
# function definitions and nothing is installed.
set -eu

fail() {
    printf 'Error: %s\n' "$*" >&2
    exit 1
}

main() {
    repo=psicomante/agentflockr
    version=${AGENTFLOCKR_VERSION:-latest}
    install_dir=${AGENTFLOCKR_INSTALL_DIR:-${HOME:?HOME must be set}/.local/bin}

    case "$(uname -s)" in
        Darwin) os=Darwin ;;
        Linux) os=Linux ;;
        *) fail 'only macOS and Linux are supported.' ;;
    esac
    case "$(uname -m)" in
        arm64|aarch64) arch=arm64 ;;
        x86_64|amd64) arch=x86_64 ;;
        *) fail "unsupported CPU architecture: $(uname -m)" ;;
    esac
    # A shell running under Rosetta on Apple silicon reports x86_64. The native
    # build is the right one there. sysctl lives in /usr/sbin, which a minimal
    # PATH may lack.
    if [ "$os" = Darwin ] && [ "$arch" = x86_64 ] &&
        [ "$(PATH="$PATH:/usr/sbin:/sbin" sysctl -n sysctl.proc_translated 2>/dev/null || true)" = 1 ]; then
        printf '%s\n' 'This shell runs under Rosetta; installing the native Apple silicon build.'
        arch=arm64
    fi
    for tool in curl tar awk; do
        command -v "$tool" >/dev/null 2>&1 || fail "$tool is required."
    done
    if command -v sha256sum >/dev/null 2>&1; then
        sha256() { sha256sum "$1" | awk '{ print $1 }'; }
    elif command -v shasum >/dev/null 2>&1; then
        sha256() { shasum -a 256 "$1" | awk '{ print $1 }'; }
    else
        fail 'sha256sum or shasum is required.'
    fi

    if [ "$version" = latest ]; then
        # Ask once which release is the latest. Downloading the archive and the
        # checksums each from releases/latest could mix two releases if one is
        # published in between. GitHub redirects to .../releases/tag/TAG, or to
        # .../releases when nothing is published yet.
        resolved=$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest") ||
            fail "could not reach https://github.com/$repo/releases/latest"
        version=${resolved##*/}
        case "$version" in
            v[0-9]*) ;;
            *) fail "no agentflockr release is published yet. Build from source instead: https://github.com/$repo#build-from-source" ;;
        esac
    fi
    base="https://github.com/$repo/releases/download/$version"
    asset="agentflockr_${os}_${arch}.tar.gz"

    tmp=$(mktemp -d)
    staged=
    trap 'rm -rf "$tmp" ${staged:+"$staged"}' EXIT
    trap 'exit 1' HUP INT TERM

    printf 'Downloading %s (%s)...\n' "$asset" "$version"
    curl -fsSL -o "$tmp/$asset" "$base/$asset" || fail "could not download $base/$asset"
    curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail "could not download $base/checksums.txt"

    expected=$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/checksums.txt")
    [ -n "$expected" ] || fail "$asset is not listed in checksums.txt."
    [ "$(sha256 "$tmp/$asset")" = "$expected" ] || fail "checksum mismatch for $asset; nothing was installed."
    tar -xzf "$tmp/$asset" -C "$tmp" agentflockr

    mkdir -p "$install_dir"
    [ -w "$install_dir" ] || fail "$install_dir is not writable."
    # Stage beside the destination and check the binary runs before replacing a
    # working one. Staying on the same file system keeps the move an atomic rename.
    staged="$install_dir/.agentflockr-install.$$"
    cp "$tmp/agentflockr" "$staged"
    chmod 0755 "$staged"
    installed=$("$staged" --version) || fail 'the downloaded binary does not run; nothing was installed.'
    mv -f "$staged" "$install_dir/agentflockr"
    staged=

    printf 'Installed %s into %s\n' "$installed" "$install_dir"
    case ":${PATH-}:" in
        *":$install_dir:"*) printf '%s\n' 'Run: agentflockr' ;;
        *)
            printf '%s\n' 'Add this directory to your PATH in your shell configuration:'
            printf '  %s\n' "$install_dir"
            ;;
    esac
}

main "$@"
