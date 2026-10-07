#!/bin/sh
# Provisions the base image inside the gVisor build sandbox as root (see
# internal/image/build.go). Inputs come from internal/image/recipe.go via
# the environment: CLAUDENBOXEN_PACKAGES, CLAUDENBOXEN_CLAUDE_REPO,
# CLAUDENBOXEN_CLAUDE_KEY_URL, CLAUDENBOXEN_CLAUDE_KEY_SHA256.
set -eu

# Rootless gVisor can't give a file a non-root owner, so apk reports
# "failed to preserve ... owner" for setuid/setgid helpers and exits
# nonzero even though every package installed. Accept that outcome only
# when each requested package is actually present.
apk_add() {
	if ! apk add --no-cache --no-progress "$@"; then
		for p in "$@"; do
			apk info -e "$p" >/dev/null || {
				echo "build.sh: $p failed to install" >&2
				return 1
			}
		done
	fi
}

# shellcheck disable=SC2086  # CLAUDENBOXEN_PACKAGES is a space-separated list by design
apk_add $CLAUDENBOXEN_PACKAGES

# Claude Code's own signed apk repository. The key is pinned by checksum so
# a compromised download can't quietly substitute a different signer.
key=/etc/apk/keys/$(basename "$CLAUDENBOXEN_CLAUDE_KEY_URL")
wget -q -O "$key" "$CLAUDENBOXEN_CLAUDE_KEY_URL"
echo "$CLAUDENBOXEN_CLAUDE_KEY_SHA256  $key" | sha256sum -c - >/dev/null
echo "$CLAUDENBOXEN_CLAUDE_REPO" >>/etc/apk/repositories
apk_add claude-code

echo "build.sh: done"
