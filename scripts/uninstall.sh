#!/bin/sh
# Removes the Doubletake desktop app installed by install.sh:
#
#   curl -fsSL https://raw.githubusercontent.com/ValentinHerrmann/doubletake-ui/main/scripts/uninstall.sh | sh
#
# Pairing credentials in ~/.config/doubletake are kept.
set -eu

PREFIX=${PREFIX:-$HOME/.local}

pkill -x doubletake-ui 2>/dev/null || true
pkill -x doubletake 2>/dev/null || true

rm -f "$PREFIX/bin/doubletake" "$PREFIX/bin/doubletake-ui" \
	"$PREFIX/share/applications/doubletake-ui.desktop" \
	"$PREFIX/share/icons/hicolor/scalable/apps/doubletake.svg" \
	"${XDG_CONFIG_HOME:-$HOME/.config}/autostart/doubletake-ui.desktop"
if command -v update-desktop-database >/dev/null; then update-desktop-database -q "$PREFIX/share/applications" || true; fi
if command -v gtk-update-icon-cache >/dev/null; then gtk-update-icon-cache -q -t "$PREFIX/share/icons/hicolor" || true; fi

echo "Doubletake removed from $PREFIX. Pairing credentials remain in ~/.config/doubletake."
