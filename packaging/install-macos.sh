#!/bin/bash
set -euo pipefail
[[ "$(uname -s)" == Darwin ]] || { echo 'macOS only' >&2; exit 1; }
PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
AGENT_FILE="$HOME/Library/LaunchAgents/com.storehub.auth-refresh.plist"
AGENT_TARGET="gui/$(id -u)/com.storehub.auth-refresh"
umask 077
mkdir -p "$HOME/.local/bin" "$HOME/.storehub" "$HOME/Library/LaunchAgents"
BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "$BUILD_DIR"' EXIT
cd "$PROJECT_DIR"
go build -o "$BUILD_DIR/storehub-auth" .
cp packaging/com.storehub.auth-refresh.plist "$BUILD_DIR/agent.plist"
/usr/libexec/PlistBuddy -c "Set :ProgramArguments:0 $HOME/.local/bin/storehub-auth" "$BUILD_DIR/agent.plist"
/usr/libexec/PlistBuddy -c "Set :StandardOutPath $HOME/.storehub/refresh.log" "$BUILD_DIR/agent.plist"
/usr/libexec/PlistBuddy -c "Set :StandardErrorPath $HOME/.storehub/refresh.error.log" "$BUILD_DIR/agent.plist"
plutil -lint "$BUILD_DIR/agent.plist"
if launchctl print "$AGENT_TARGET" >/dev/null 2>&1; then
  launchctl bootout "$AGENT_TARGET"
fi
install -m 0755 "$BUILD_DIR/storehub-auth" "$HOME/.local/bin/storehub-auth"
install -m 0600 "$BUILD_DIR/agent.plist" "$AGENT_FILE"
launchctl bootstrap "gui/$(id -u)" "$AGENT_FILE"
echo 'Installed hourly refresh. If credentials have expired, run ~/.local/bin/storehub-auth --force.'
