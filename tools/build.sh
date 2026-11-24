#!/bin/sh
# Builds the release binaries into dist/, writes their SHA-256 to
# hooks/SHA256SUMS (what the launcher checks downloads against), and copies
# them into bin/ so a clone works as a plugin with --plugin-dir.
#
# The build is reproducible: same source + same Go version (go.mod) = same
# bytes on any machine. The release workflow rebuilds on GitHub and refuses
# to publish if its bytes differ from the SHA256SUMS committed here.
#
# Run from Git Bash or a Linux shell: sh tools/build.sh

set -eu
cd "$(dirname -- "$0")/.."

# Pin everything that changes the output; this machine's shell may export
# GOARCH=386 or GOFLAGS.
export CGO_ENABLED=0 GOARCH=amd64 GOAMD64=v1 GOFLAGS= GOEXPERIMENT= GOWORK=off
# The go line in go.mod is the exact toolchain; a newer Go gives other bytes.
GOTOOLCHAIN=go$(awk '/^go /{print $2}' go.mod)
export GOTOOLCHAIN
flags='-trimpath -buildvcs=false'

mkdir -p dist bin
GOOS=windows go build $flags -ldflags='-H windowsgui -s -w -buildid=' -o dist/sessionlights-windows-amd64.exe .
GOOS=linux go build $flags -ldflags='-s -w -buildid=' -o dist/sessionlights-linux-amd64 .

(cd dist && sha256sum sessionlights-windows-amd64.exe sessionlights-linux-amd64) |
	sed 's/ \*/  /' >hooks/SHA256SUMS

# Windows refuses to overwrite a running exe; a running strip only costs the
# local copy, not the release.
cp dist/sessionlights-windows-amd64.exe bin/sessionlights.exe 2>/dev/null ||
	echo "bin/sessionlights.exe is in use (strip running?); left as is" >&2
cp dist/sessionlights-linux-amd64 bin/sessionlights

cat hooks/SHA256SUMS
