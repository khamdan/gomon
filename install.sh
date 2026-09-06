#!/bin/sh
#
# install.sh - install gomon as a systemd service and enable it at boot.
#
#   sudo ./install.sh              install on the default port (8080)
#   sudo ./install.sh 9090         install on port 9090
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

case "${1:-}" in
	uninstall)
		systemctl disable --now gomon 2>/dev/null || true
		rm -f "$UNIT" "$BIN/gomon" "$BIN/gomonctl"
		systemctl daemon-reload
		echo "gomon removed"
		exit 0
		;;
	"") ;;
	*[!0-9]*) echo "usage: $0 [PORT|uninstall]" >&2; exit 1 ;;
	*)
		PORT=$1
		[ "$PORT" -ge 1 ] && [ "$PORT" -le 65535 ] ||
			{ echo "port out of range: $PORT" >&2; exit 1; }
		;;
esac

install -m755 "$DIR/gomon"    "$BIN/gomon"
install -m755 "$DIR/gomonctl" "$BIN/gomonctl"

# The port lives in the unit's ExecStart, so bake the chosen one in as the
# unit is copied rather than shipping a second config file for one number.
sed "s|-listen :[0-9]*|-listen :$PORT|" "$DIR/gomon.service" > "$UNIT"

# Ports below 1024 are privileged, and this service deliberately runs as
# "nobody". Granting just CAP_NET_BIND_SERVICE is the narrow way to allow it;
# without this the bind fails with EACCES.
if [ "$PORT" -lt 1024 ]; then
	sed -i "s|^\[Install\]|AmbientCapabilities=CAP_NET_BIND_SERVICE\nCapabilityBoundingSet=CAP_NET_BIND_SERVICE\n\n[Install]|" "$UNIT"
fi
chmod 644 "$UNIT"

systemctl daemon-reload
systemctl enable --now gomon

# The service runs as "nobody", so a failure here is almost always a port
# already in use - show it rather than claiming success.
if ! systemctl is-active --quiet gomon; then
	echo "gomon failed to start:" >&2
	systemctl --no-pager --lines=15 status gomon >&2 || true
	exit 1
fi

IP=$(ip -4 -o addr show scope global 2>/dev/null | awk '{split($4,a,"/"); print a[1]; exit}')
echo "gomon installed, enabled at boot, and running"
echo "  http://${IP:-localhost}:$PORT"
