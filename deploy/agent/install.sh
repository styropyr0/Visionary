#!/bin/sh
# Installs the Visionary host agent as a systemd service.
#
#   curl -fsSL https://raw.githubusercontent.com/styropyr0/Visionary/main/deploy/agent/install.sh \
#     | sudo VISIONARY_URL=https://visionary.example.com VISIONARY_KEY=<server key> sh
#
# VISIONARY_AGENT_BINARY=/path/to/visionary-agent installs a binary you built
# (go build ./cmd/visionary-agent) instead of downloading the release.
set -eu

: "${VISIONARY_URL:?set VISIONARY_URL to the Visionary server URL}"
: "${VISIONARY_KEY:?set VISIONARY_KEY to a server ingest key}"
REPO="${VISIONARY_AGENT_REPO:-styropyr0/Visionary}"
VERSION="${VISIONARY_AGENT_VERSION:-latest}"

[ "$(id -u)" = 0 ] || { echo "run as root (sudo)" >&2; exit 1; }
command -v systemctl >/dev/null || { echo "systemd not found; run /usr/local/bin/visionary-agent under your init system instead" >&2; exit 1; }

case "$(uname -m)" in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac

if [ -n "${VISIONARY_AGENT_BINARY:-}" ]; then
  install -m 0755 "$VISIONARY_AGENT_BINARY" /usr/local/bin/visionary-agent
else
  if [ "$VERSION" = latest ]; then
    URL="https://github.com/$REPO/releases/latest/download/visionary-agent-linux-$ARCH"
  else
    URL="https://github.com/$REPO/releases/download/$VERSION/visionary-agent-linux-$ARCH"
  fi
  echo "downloading $URL"
  TMP="$(mktemp)"
  curl -fsSL "$URL" -o "$TMP"
  install -m 0755 "$TMP" /usr/local/bin/visionary-agent
  rm -f "$TMP"
fi

# Settings: kept on reinstall except URL and key, which are replaced.
ENV=/etc/visionary-agent.env
touch "$ENV"
chmod 0600 "$ENV"
grep -v -e '^VISIONARY_URL=' -e '^VISIONARY_KEY=' "$ENV" > "$ENV.tmp" || true
{
  echo "VISIONARY_URL=$VISIONARY_URL"
  echo "VISIONARY_KEY=$VISIONARY_KEY"
  for v in VISIONARY_HOST VISIONARY_SERVICE VISIONARY_AGENT_UNITS VISIONARY_AGENT_INTERVAL VISIONARY_AGENT_REPORT VISIONARY_AGENT_SECURITY VISIONARY_AGENT_AUTH_LOG VISIONARY_AGENT_DOCKER VISIONARY_DISK_PATHS; do
    eval "val=\${$v:-}"
    if [ -n "$val" ]; then
      sed -i "/^$v=/d" "$ENV.tmp"
      echo "$v=$val"
    fi
  done
} >> "$ENV.tmp"
mv "$ENV.tmp" "$ENV"

UNIT_URL="https://raw.githubusercontent.com/$REPO/main/deploy/agent/visionary-agent.service"
if [ -f "$(dirname "$0")/visionary-agent.service" ]; then
  install -m 0644 "$(dirname "$0")/visionary-agent.service" /etc/systemd/system/visionary-agent.service
else
  curl -fsSL "$UNIT_URL" -o /etc/systemd/system/visionary-agent.service
fi

systemctl daemon-reload
systemctl enable --now visionary-agent
systemctl restart visionary-agent
echo "visionary-agent $(/usr/local/bin/visionary-agent -version | cut -d' ' -f2) is running; it reports to $VISIONARY_URL"
