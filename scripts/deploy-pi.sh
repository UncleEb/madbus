#!/usr/bin/env bash
#
# Deploy Madbus to the Raspberry Pi rig: cross-compile for the Pi, push the
# binary, restart the systemd service. Fast dev loop for "edit here, run there".
#
# It deploys ONLY the binary — it deliberately does NOT overwrite the Pi's
# config.json or profiles/, because those are managed on the Pi itself (device
# management + profiles via the web UI). Push those manually for first-time
# setup only.
#
# Target Pi comes from $MADBUS_PI or a local .pi-target file (both gitignored),
# as "user@host". Arch defaults to arm64 (64-bit Pi); override with $MADBUS_ARCH.
#
#   ./scripts/deploy-pi.sh
#   MADBUS_PI=madbus@192.168.1.7 ./scripts/deploy-pi.sh
#
set -euo pipefail
cd "$(dirname "$0")/.."

TARGET="${MADBUS_PI:-$(cat .pi-target 2>/dev/null || true)}"
if [ -z "$TARGET" ]; then
  echo "No Pi target. Set MADBUS_PI=user@host or write it to .pi-target" >&2
  exit 1
fi
ARCH="${MADBUS_ARCH:-arm64}"
SSH="ssh -o BatchMode=yes -o ConnectTimeout=10"
BIN="$(mktemp -d)/madbus"

echo "building linux/$ARCH…"
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -o "$BIN" .

echo "pushing to $TARGET…"
# Copy to a temp name, then rename over the target: you can't overwrite a
# running executable (ETXTBSY), but rename relinks the path fine and the restart
# execs the new binary.
scp -q -o BatchMode=yes "$BIN" "$TARGET:~/madbus/madbus.new"

echo "restarting service…"
$SSH "$TARGET" 'mv ~/madbus/madbus.new ~/madbus/madbus && chmod +x ~/madbus/madbus && sudo systemctl restart madbus && sleep 2 && echo "  active: $(systemctl is-active madbus)"'

echo "done. logs:  ssh $TARGET 'journalctl -u madbus -f'"
