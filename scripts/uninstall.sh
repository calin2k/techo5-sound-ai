#!/bin/sh
# Uninstall techo5-sound-ai from a TECHO5 device.
set -eu

uninstall_on_device() {
	if [ -f /run/techo5-sound-ai.lock ]; then
		pid=$(cat /run/techo5-sound-ai.lock 2>/dev/null || true)
		[ -n "$pid" ] && kill "$pid" 2>/dev/null || true
	fi
	rm -f /run/techo5-sound-ai.lock /run/techo5-sound-ai.sock

	if [ -f /data/techo5-linux/external-asp/bin/external-asp.orig ]; then
		mv -f /data/techo5-linux/external-asp/bin/external-asp.orig /data/techo5-linux/external-asp/bin/external-asp
	else
		rm -f /data/techo5-linux/external-asp/bin/external-asp
		if [ -f /data/misc/techo5/state.json ] && grep -q '"asp": "external"' /data/misc/techo5/state.json 2>/dev/null; then
			sed -i 's/"asp": "external"/"asp": "builtin"/' /data/misc/techo5/state.json
			kill $(pidof techo5) 2>/dev/null || true
		fi
	fi

	if [ -f /data/techo5-linux/rc.local ]; then
		grep -v "techo5-sound-ai" /data/techo5-linux/rc.local > /data/techo5-linux/rc.local.tmp || true
		mv -f /data/techo5-linux/rc.local.tmp /data/techo5-linux/rc.local
		chmod 755 /data/techo5-linux/rc.local
	fi

	rm -rf /data/techo5-sound-ai
	echo "techo5-sound-ai removed."
}

if [ "${1:-}" != "" ]; then
	ssh "root@$1" "$(typeset -f uninstall_on_device); uninstall_on_device"
else
	uninstall_on_device
fi
