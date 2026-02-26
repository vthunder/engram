#!/usr/bin/env bash
# Generates and installs the Engram launchd plist for macOS.
# Run from anywhere — paths are derived from this script's location.
#
# Usage:
#   scripts/install-launchd.sh [--uninstall]
#
# The service is named com.engram and logs to ~/Library/Logs/engram.log.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENGRAM_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BINARY="$ENGRAM_DIR/engram"
PLIST_LABEL="com.engram"
PLIST_DEST="$HOME/Library/LaunchAgents/$PLIST_LABEL.plist"
LOG_FILE="$HOME/Library/Logs/engram.log"

# --- uninstall ---
if [[ "${1:-}" == "--uninstall" ]]; then
    if launchctl list "$PLIST_LABEL" &>/dev/null; then
        echo "Stopping $PLIST_LABEL..."
        launchctl bootout "gui/$(id -u)/$PLIST_LABEL" || true
    fi
    if [[ -f "$PLIST_DEST" ]]; then
        rm "$PLIST_DEST"
        echo "Removed $PLIST_DEST"
    fi
    echo "Uninstalled."
    exit 0
fi

# --- pre-flight ---
if [[ ! -f "$BINARY" ]]; then
    echo "Error: engram binary not found at $BINARY" >&2
    echo "Build it first: make build" >&2
    exit 1
fi

# --- migrate old label if present ---
OLD_LABEL="com.bud.engram"
OLD_PLIST="$HOME/Library/LaunchAgents/$OLD_LABEL.plist"
if launchctl list "$OLD_LABEL" &>/dev/null; then
    echo "Stopping old service ($OLD_LABEL)..."
    launchctl bootout "gui/$(id -u)/$OLD_LABEL" || true
fi
if [[ -f "$OLD_PLIST" ]]; then
    echo "Removing old plist $OLD_PLIST..."
    rm "$OLD_PLIST"
fi

# --- generate plist ---
cat > "$PLIST_DEST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>$PLIST_LABEL</string>

    <key>ProgramArguments</key>
    <array>
        <string>$BINARY</string>
    </array>

    <key>WorkingDirectory</key>
    <string>$ENGRAM_DIR</string>

    <key>EnvironmentVariables</key>
    <dict>
        <key>HOME</key>
        <string>$HOME</string>
        <key>PATH</key>
        <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
    </dict>

    <key>RunAtLoad</key>
    <true/>

    <key>KeepAlive</key>
    <true/>

    <key>StandardOutPath</key>
    <string>$LOG_FILE</string>

    <key>StandardErrorPath</key>
    <string>$LOG_FILE</string>

    <key>ProcessType</key>
    <string>Background</string>
</dict>
</plist>
EOF

echo "Wrote $PLIST_DEST"

# --- load ---
launchctl bootstrap "gui/$(id -u)" "$PLIST_DEST"
echo "Started $PLIST_LABEL"
echo "Logs: $LOG_FILE"
