#!/bin/sh
# SessionLights launcher. The plugin ships no binaries: this script fetches
# the one for this plugin version from the matching GitHub Release, checks it
# against the SHA-256 pinned in hooks/SHA256SUMS, and puts it in bin/.
#
#   ensure.sh hook   fetch if missing, then run `sessionlights hook` on this
#                    stdin (registered for SessionStart, so the first session
#                    after an install or update downloads it)
#   ensure.sh        fetch if missing, nothing else (exit 1 if that failed)
#
# A binary already in bin/ is never touched, so a local build stays put.
# Hook mode never fails a session: errors go to stderr and it exits 0.
# Nothing goes to stdout, because SessionStart stdout reaches Claude's context.

set -u
mode=${1:-}

die() {
	echo "sessionlights: $*" >&2
	[ "$mode" = hook ] && exit 0
	exit 1
}

root=${CLAUDE_PLUGIN_ROOT:-$(dirname -- "$0")/..}
command -v cygpath >/dev/null 2>&1 && root=$(cygpath -u "$root")
root=$(CDPATH= cd -- "$root" && pwd) || die "plugin folder not found"

case "$(uname -s)" in
Linux) os=linux exe= ;;
MINGW* | MSYS* | CYGWIN*) os=windows exe=.exe ;;
*) die "no binary for $(uname -s); see the README" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
*) die "no binary for $(uname -m); see the README" ;;
esac

bin="$root/bin/sessionlights$exe"
asset="sessionlights-$os-$arch$exe"

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256
	else
		openssl dgst -sha256 -r
	fi <"$1" | awk '{print $1}'
}

fetch() {
	ver=$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$root/.claude-plugin/plugin.json" | head -n 1)
	[ -n "$ver" ] || die "no version in plugin.json"
	want=$(tr -d '\r' <"$root/hooks/SHA256SUMS" | awk -v a="$asset" '{ n = $2; sub(/^\*/, "", n) } n == a { print $1 }')
	[ -n "$want" ] || die "no checksum for $asset in hooks/SHA256SUMS"

	base=${SESSIONLIGHTS_RELEASE_URL:-https://github.com/Carltk/sessionlights/releases/download/v$ver}
	url="$base/$asset"
	tmp="$bin.$$.tmp"
	trap 'rm -f "$tmp"' EXIT
	trap 'exit 1' INT TERM
	mkdir -p "$root/bin" || die "cannot create $root/bin"
	echo "sessionlights: downloading $url" >&2
	# One attempt, bounded under the hook's 90 s timeout. A failure leaves bin/
	# empty, so the next session start simply tries again.
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --connect-timeout 10 --max-time 75 -o "$tmp" "$url"
	elif command -v wget >/dev/null 2>&1; then
		if command -v timeout >/dev/null 2>&1; then
			timeout 75 wget -q -t 1 -T 20 -O "$tmp" "$url"
		else
			wget -q -t 1 -T 20 -O "$tmp" "$url"
		fi
	else
		die "need curl or wget to download $url"
	fi || {
		rm -f "$tmp"
		die "download failed: $url"
	}

	got=$(sha256 "$tmp")
	if [ "$got" != "$want" ]; then
		rm -f "$tmp"
		die "checksum mismatch for $asset (got $got, want $want); nothing installed"
	fi
	chmod +x "$tmp" || die "cannot make $tmp executable"
	# Another session, or a local build, may have put a binary there while we
	# downloaded: keep it. On Windows a running exe cannot be replaced anyway.
	[ -f "$bin" ] && return 0
	if ! mv -f "$tmp" "$bin" 2>/dev/null; then
		[ -f "$bin" ] || die "could not install $bin"
	fi
}

[ -f "$bin" ] || fetch
[ "$mode" = hook ] || exit 0
exec "$bin" hook
