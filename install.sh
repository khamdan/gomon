#!/bin/sh
#
# install.sh - install gomon as a systemd service and enable it at boot.
#
#   sudo ./install.sh              install, enable at boot, start now
#   sudo ./install.sh uninstall    stop, disable, remove everything
#
# Run this from inside the unpacked release tarball - it installs the files
# sitting next to it.

set -e

DIR=$(cd "$(dirname "$0")" && pwd)
BIN=/usr/local/bin
UNIT=/etc/systemd/system/gomon.service
PORT=8080

[ "$(id -u)" -eq 0 ] || { echo "needs root: sudo $0 $*" >&2; exit 1; }
command -v systemctl >/dev/null 2>&1 || { echo "this installer needs systemd" >&2; exit 1; }

if [ "${1:-}" = uninstall ]; then
	systemctl disable --now gomon 2>/dev/null || true
	rm -f "$UNIT" "$BIN/gomon" "$BIN/gomonctl"
	systemctl daemon-reload
	echo "gomon removed"
	exit 0
fi

install -m755 "$DIR/gomon"         "$BIN/gomon"
install -m755 "$DIR/gomonctl"      "$BIN/gomonctl"
install -m644 "$DIR/gomon.service" "$UNIT"

systemctl daemon-reload
systemctl enable --now gomon

# The service runs as "nobody", so a failure here is almost always a missing
# nogroup or a port already in use - show it rather than claiming success.
if ! systemctl is-active --quiet gomon; then
	echo "gomon failed to start:" >&2
	systemctl --no-pager --lines=15 status gomon >&2 || true
	exit 1
fi

IP=$(ip -4 -o addr show scope global 2>/dev/null | awk '{split($4,a,"/"); print a[1]; exit}')
echo "gomon installed, enabled at boot, and running"
echo "  http://${IP:-localhost}:$PORT"
