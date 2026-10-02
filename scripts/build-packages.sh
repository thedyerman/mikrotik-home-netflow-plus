#!/bin/sh
# Builds the release packages into dist/: for each architecture a Debian
# package and a tar.gz of the standalone binary, plus SHA256SUMS.
#
#   scripts/build-packages.sh <version> [amd64|arm64|armhf ...]
#
# Needs Go and, if web/dist is not built yet, Node. nfpm is fetched with
# "go run" on first use. The package layout is in packaging/nfpm.yaml.
set -eu
cd "$(dirname "$0")/.."

VERSION=${1:?usage: scripts/build-packages.sh <version> [amd64 arm64 armhf ...]}
shift
ARCHES=${*:-amd64 arm64 armhf}
NFPM=github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.47.0
NAME=mikrotik-home-netflow-plus
STAGE=dist/stage

if [ ! -f web/dist/index.html ]; then
    (cd web && npm ci && npm run build)
fi
mkdir -p dist

for arch in $ARCHES; do
    case $arch in
        amd64) goarch=amd64; goarm= ;;
        arm64) goarch=arm64; goarm= ;;
        # ARMv6 code runs on every 32-bit Raspberry Pi, including the original
        # and the Zero, and costs nothing measurable on newer ones.
        armhf) goarch=arm; goarm=6 ;;
        *) echo "unknown architecture: $arch (amd64, arm64 or armhf)" >&2; exit 2 ;;
    esac
    echo "building $NAME $VERSION for $arch"
    rm -rf "$STAGE"
    mkdir -p "$STAGE"
    CGO_ENABLED=0 GOOS=linux GOARCH=$goarch GOARM=$goarm \
        go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
        -o "$STAGE/$NAME" ./cmd/$NAME
    # Debian wants a changelog; each release's notes live on GitHub.
    printf '%s (%s) stable; urgency=medium\n\n  * Release %s; release notes at\n    https://github.com/thedyerman/%s/releases\n\n -- kc <kc@dyertech.ca>  %s\n' \
        "$NAME" "$VERSION" "$VERSION" "$NAME" "$(LC_ALL=C date -u '+%a, %d %b %Y %H:%M:%S +0000')" \
        | gzip -9n > "$STAGE/changelog.gz"
    for page in packaging/*.8; do
        gzip -9nc "$page" > "$STAGE/$(basename "$page").gz"
    done
    VERSION=$VERSION NFPM_ARCH=$arch \
        go run "$NFPM" package --config packaging/nfpm.yaml --packager deb --target dist/
    cp LICENSE NOTICE "$STAGE/"
    COPYFILE_DISABLE=1 tar -C "$STAGE" -czf "dist/${NAME}_${VERSION}_linux_${arch}.tar.gz" "$NAME" LICENSE NOTICE
done
rm -rf "$STAGE"

cd dist
if command -v sha256sum >/dev/null 2>&1; then
    sha256sum ./*.deb ./*.tar.gz > SHA256SUMS
else
    shasum -a 256 ./*.deb ./*.tar.gz > SHA256SUMS
fi
ls -l ./*.deb ./*.tar.gz
