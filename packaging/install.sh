#!/bin/sh
# Installs the Doubletake desktop app from this bundle. Defaults to ~/.local so
# no root is needed; set PREFIX=/usr/local (and run as root) for all users.
#
#   ./install.sh               install
#   ./install.sh --uninstall   remove
set -eu

PREFIX=${PREFIX:-$HOME/.local}
BINDIR=$PREFIX/bin
APPDIR=$PREFIX/share/applications
ICONDIR=$PREFIX/share/icons/hicolor/scalable/apps
HERE=$(cd "$(dirname "$0")" && pwd)

refresh_caches() {
	if command -v update-desktop-database >/dev/null; then update-desktop-database -q "$APPDIR" || true; fi
	if command -v gtk-update-icon-cache >/dev/null; then gtk-update-icon-cache -q -t "$PREFIX/share/icons/hicolor" || true; fi
}

if [ "${1:-}" = "--uninstall" ]; then
	rm -f "$BINDIR/doubletake" "$BINDIR/doubletake-ui" "$APPDIR/doubletake-ui.desktop" "$ICONDIR/doubletake.svg" \
		"${XDG_CONFIG_HOME:-$HOME/.config}/autostart/doubletake-ui.desktop"
	refresh_caches
	echo "Doubletake removed from $PREFIX."
	exit 0
fi

install -d "$BINDIR" "$APPDIR" "$ICONDIR"
# doubletake-ui starts the doubletake daemon next to it.
install -m 755 "$HERE/bin/doubletake" "$HERE/bin/doubletake-ui" "$BINDIR/"
# Absolute Exec: desktop sessions often lack ~/.local/bin on PATH.
sed "s|^Exec=doubletake-ui|Exec=$BINDIR/doubletake-ui|" "$HERE/share/applications/doubletake-ui.desktop" >"$APPDIR/doubletake-ui.desktop"
chmod 644 "$APPDIR/doubletake-ui.desktop"
install -m 644 "$HERE/share/icons/hicolor/scalable/apps/doubletake.svg" "$ICONDIR/"
refresh_caches

echo "Doubletake installed to $PREFIX. Start it from your app menu or run $BINDIR/doubletake-ui."

if ! command -v gst-launch-1.0 >/dev/null; then
	echo "warning: GStreamer not found; install GStreamer 1.0 with the base/good/bad/ugly/libav plugins (see README)." >&2
fi
if ! ldconfig -p 2>/dev/null | grep -q 'libgtk-4\.so\.1'; then
	echo "warning: GTK 4 not found; install it to run doubletake-ui (Arch: gtk4, Debian/Ubuntu: libgtk-4-1)." >&2
fi
