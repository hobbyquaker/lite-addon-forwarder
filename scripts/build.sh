#!/bin/sh
#
#   Builds the addon packages: dist/forwarder-<arch>-<version>.tar.gz and .sha256 for aarch64,
#   x86_64 and armv7l (the asset names openccu-lite's catalogue resolves by uname -m).
#
#     VERSION=0.1.0 scripts/build.sh            # all three
#     ARCHES=x86_64 scripts/build.sh            # one
#     SKIP_UI=1 scripts/build.sh                # keep addon/forwarder/app as it is
#
#   The configuration page is built with npm in ui/ and copied to addon/forwarder/app (the daemon
#   serves it at /addons/forwarder/); the manifest's icons and the 503 page live in
#   addon/forwarder/www, where the catalogue reads the icons from the repository at the release
#   tag; the daemon is a static Go binary (CGO_ENABLED=0) per architecture.
#
set -e
cd "$(dirname "$0")/.."
VERSION=${VERSION:-0.0.0-dev}
ARCHES=${ARCHES:-aarch64 x86_64 armv7l}
GO=${GO:-go}

if [ -z "$SKIP_UI" ]; then
    (cd ui && npm ci --no-audit --no-fund && npm run build)
    rm -rf addon/forwarder/app
    mkdir -p addon/forwarder/app
    cp -a ui/dist/. addon/forwarder/app/
fi
cp -a addon/static/. addon/forwarder/www/
printf 'VERSION_ADDON=%s\n' "$VERSION" > addon/forwarder/versions

mkdir -p dist
for arch in $ARCHES; do
    case $arch in
        aarch64) goarch=arm64; goarm= ;;
        x86_64) goarch=amd64; goarm= ;;
        armv7l) goarch=arm; goarm=7 ;;
        *) echo "unknown architecture $arch" >&2; exit 1 ;;
    esac
    mkdir -p addon/forwarder/bin
    rm -f addon/forwarder/bin/forwarder
    CGO_ENABLED=0 GOOS=linux GOARCH=$goarch GOARM=$goarm $GO build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o addon/forwarder/bin/forwarder ./cmd/forwarder

    out=dist/forwarder-$arch-$VERSION.tar.gz
    stage=$(mktemp -d)
    cp -a addon/update_script addon/openccu-lite.json addon/forwarder "$stage/"
    rm -rf "$stage/forwarder/var" "$stage/forwarder/etc/forwarder.json" "$stage/forwarder/etc/secrets.json"
    find "$stage" \( -name '._*' -o -name '.DS_Store' \) -exec rm -f {} +
    chmod 755 "$stage/update_script" "$stage/forwarder/rc.d" "$stage/forwarder/bin/forwarder"
    COPYFILE_DISABLE=1 tar --owner=root --group=root --sort=name --mtime='2026-01-01 00:00Z' -czf "$out" -C "$stage" .
    rm -rf "$stage"
    (cd dist && sha256sum "$(basename "$out")" > "$(basename "$out").sha256")
    echo "built $out"
done
