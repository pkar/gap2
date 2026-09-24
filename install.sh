#!/bin/sh
# Install the newest published release, including prereleases. Set
# GAP2_VERSION to pin a tag or GAP2_INSTALL_DIR to choose a destination.
set -eu

repo=pkar/gap2
binary=airplay2-receiver
fail() { echo "gap2 install: $*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || fail "curl is required"
tmp=$(mktemp -d) || fail "could not create temporary directory"
trap 'rm -rf "$tmp"' 0
trap 'exit 1' 1 2 3 15

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$os" in linux|darwin) ;; *) fail "unsupported OS: $os" ;; esac
case "$arch" in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; esac

tag=${GAP2_VERSION:-}
if [ -z "$tag" ]; then
  curl -fsSL -H 'Accept: application/vnd.github+json' \
    -o "$tmp/releases.json" "https://api.github.com/repos/$repo/releases?per_page=1" \
    || fail "could not list releases"
  tag=$(sed -n 's/^[[:space:]]*"tag_name": *"\([^"]*\)".*/\1/p' "$tmp/releases.json" | sed -n '1p')
fi
case "$tag" in v[0-9]*) ;; *) fail "no valid release tag found" ;; esac
case "$tag" in *[!a-zA-Z0-9.-]*) fail "invalid release tag" ;; esac

asset="$binary-$os-$arch"
base="https://github.com/$repo/releases/download/$tag"
case "$os/$arch" in
  linux/amd64|linux/arm64|darwin/arm64)
    curl -fsSL -o "$tmp/$binary" "$base/$asset" || fail "could not download $asset"
    curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail "could not download checksums"
    expected=
    while read -r digest name extra; do
      if [ "$name" = "$asset" ]; then
        [ -z "$expected" ] && [ -z "$extra" ] || fail "ambiguous checksum entry"
        [ "${#digest}" -eq 64 ] || fail "invalid checksum"
        case "$digest" in *[!0-9a-f]*) fail "invalid checksum" ;; esac
        expected=$digest
      fi
    done < "$tmp/checksums.txt"
    [ -n "$expected" ] || fail "missing checksum for $asset"
    if command -v sha256sum >/dev/null 2>&1; then
      actual=$(sha256sum "$tmp/$binary") || fail "checksum calculation failed"
    elif command -v shasum >/dev/null 2>&1; then
      actual=$(shasum -a 256 "$tmp/$binary") || fail "checksum calculation failed"
    else
      fail "checksum verification requires sha256sum or shasum"
    fi
    [ "${actual%% *}" = "$expected" ] || fail "checksum mismatch for $asset"
    echo "verified $asset ($tag)"
    ;;
  *)
    command -v go >/dev/null 2>&1 || fail "no binary for $os/$arch; Go is required to build from source"
    curl -fsSL -o "$tmp/source.tar.gz" "https://github.com/$repo/archive/refs/tags/$tag.tar.gz" \
      || fail "could not download source for $tag"
    mkdir "$tmp/source"
    tar -xzf "$tmp/source.tar.gz" -C "$tmp/source" --strip-components=1 \
      || fail "could not unpack source"
    (cd "$tmp/source" && CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${tag#v}" -o "$tmp/$binary" ./cmd/airplay2-receiver) \
      || fail "source build failed"
    ;;
esac

bindir=${GAP2_INSTALL_DIR:-"$HOME/.local/bin"}
mkdir -p "$bindir" || fail "could not create $bindir"
install -m 0755 "$tmp/$binary" "$bindir/$binary" || fail "could not install to $bindir"
echo "installed $bindir/$binary ($tag)"
case ":$PATH:" in *:"$bindir":*) ;; *) echo "note: $bindir is not on your PATH" >&2 ;; esac
