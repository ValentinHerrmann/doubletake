#!/bin/sh
# Updates Doubletake to the latest release:
#
#   curl -fsSL https://raw.githubusercontent.com/ValentinHerrmann/doubletake/main/scripts/update.sh | sh
set -eu

curl -fsSL https://raw.githubusercontent.com/ValentinHerrmann/doubletake/main/scripts/install.sh | sh

# A running daemon keeps the old binary until it restarts.
if pgrep -x doubletake >/dev/null 2>&1; then
	echo "note: a doubletake daemon is still running the old version; stop casting and run 'pkill -x doubletake' to restart it with the update." >&2
fi
