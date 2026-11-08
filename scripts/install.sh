#!/bin/sh
# Install techo5-sound-ai on a TECHO5 device (survives all OTA firmware updates).
#
# Usage:
#   From workstation: ./scripts/install.sh <device-ip-or-host>
#   On device:        sh install.sh
set -eu

REPO_RELEASE_URL="https://github.com/calin2k/techo5-sound-ai/releases/latest/download/techo5-sound-ai-linux-arm"
YAMNET_URL="https://www.kaggle.com/api/v1/models/google/yamnet/tfLite/classification-tflite/1/download"

install_on_device() {
	mkdir -p /data/techo5-sound-ai /data/misc/techo5/yamnet
	chmod 700 /data/techo5-sound-ai

	# Set up /data/techo5-linux/external-asp/bin/external-asp with strict 0700 root-only permissions
	# required by TECHO5's openPinned/verifyPinned check.
	mkdir -p /data/techo5-linux/external-asp/bin
	chown 0:0 /data/techo5-linux/external-asp /data/techo5-linux/external-asp/bin
	chmod 700 /data/techo5-linux/external-asp /data/techo5-linux/external-asp/bin

	if [ -f /data/techo5-linux/external-asp/bin/external-asp ] && \
	   ! grep -q "techo5-sound-ai" /data/techo5-linux/external-asp/bin/external-asp 2>/dev/null && \
	   [ ! -f /data/techo5-linux/external-asp/bin/external-asp.orig ]; then
		mv /data/techo5-linux/external-asp/bin/external-asp /data/techo5-linux/external-asp/bin/external-asp.orig
		chmod 700 /data/techo5-linux/external-asp/bin/external-asp.orig
	fi

	cp -f /data/techo5-sound-ai/techo5-sound-ai /data/techo5-linux/external-asp/bin/external-asp
	chown 0:0 /data/techo5-sound-ai/techo5-sound-ai /data/techo5-linux/external-asp/bin/external-asp
	chmod 700 /data/techo5-sound-ai/techo5-sound-ai /data/techo5-linux/external-asp/bin/external-asp

	# Hook into /data/techo5-linux/rc.local (TECHO5 v1.6.30+)
	RCLOCAL=/data/techo5-linux/rc.local
	if [ ! -f "$RCLOCAL" ]; then
		cat > "$RCLOCAL" <<'EOF'
#!/bin/sh
/data/techo5-sound-ai/techo5-sound-ai --daemon >/data/techo5-sound-ai/sound-ai.log 2>&1 &
EOF
	elif ! grep -q "techo5-sound-ai" "$RCLOCAL" 2>/dev/null; then
		printf '\n/data/techo5-sound-ai/techo5-sound-ai --daemon >/data/techo5-sound-ai/sound-ai.log 2>&1 &\n' >> "$RCLOCAL"
	fi
	chmod 755 "$RCLOCAL"

	# Enable External ASP in TECHO5 state.json if not already enabled
	if [ -f /data/misc/techo5/state.json ] && grep -q '"asp": "builtin"' /data/misc/techo5/state.json 2>/dev/null; then
		sed -i 's/"asp": "builtin"/"asp": "external"/' /data/misc/techo5/state.json
		kill $(pidof techo5) 2>/dev/null || true
	fi

	# Start or restart the daemon
	if [ -f /run/techo5-sound-ai.lock ]; then
		oldpid=$(cat /run/techo5-sound-ai.lock 2>/dev/null || true)
		[ -n "$oldpid" ] && kill "$oldpid" 2>/dev/null || true
		sleep 1
	fi
	/data/techo5-sound-ai/techo5-sound-ai --daemon >/data/techo5-sound-ai/sound-ai.log 2>&1 &
	echo "techo5-sound-ai installed and started on port 8182."
	echo "In Home Assistant, set 'Microphone ASP selected' to 'External' on this device to stream mic audio."
}

if [ "${1:-}" != "" ]; then
	HOST="$1"
	BIN="techo5-sound-ai-linux-arm"
	if [ ! -f "$BIN" ]; then
		echo "Building $BIN..."
		GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -ldflags="-s -w" -o "$BIN" ./cmd/techo5-sound-ai
	fi
	echo "Uploading to root@$HOST:/data/techo5-sound-ai/techo5-sound-ai..."
	ssh "root@$HOST" 'mkdir -p /data/techo5-sound-ai && if [ -f /run/techo5-sound-ai.lock ]; then kill $(cat /run/techo5-sound-ai.lock) 2>/dev/null || true; fi'
	cat "$BIN" | ssh "root@$HOST" 'cat > /data/techo5-sound-ai/techo5-sound-ai.new && mv -f /data/techo5-sound-ai/techo5-sound-ai.new /data/techo5-sound-ai/techo5-sound-ai'
	ssh "root@$HOST" "$(typeset -f install_on_device); install_on_device"
	echo "Open http://$HOST:8182/ to configure Room Sound AI."
	exit 0
fi

# Running directly on the TECHO5 device
mkdir -p /data/techo5-sound-ai
if [ ! -f /data/techo5-sound-ai/techo5-sound-ai ]; then
	echo "Downloading techo5-sound-ai binary..."
	wget -O /data/techo5-sound-ai/techo5-sound-ai "$REPO_RELEASE_URL"
fi
install_on_device
