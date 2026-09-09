#!/bin/sh
set -eu
: "${PREFIX:?Run this script inside Termux}"
command -v sv >/dev/null 2>&1 || { echo 'termux-services is required. Install it with your consent first.' >&2; exit 1; }
alina_binary=${1:-}
case "$alina_binary" in /*) ;; *) echo 'Pass an absolute path to the Alina executable.' >&2; exit 1;; esac
[ -x "$alina_binary" ] || { echo 'Alina binary is not executable.' >&2; exit 1; }
alina_state=${ALINA_HOME:-${XDG_CONFIG_HOME:-$HOME/.config}/alina}
case "$alina_state" in /*) ;; *) echo 'ALINA_HOME must be absolute.' >&2; exit 1;; esac
[ -f "$alina_state/config.json" ] || { echo 'Run alina setup first.' >&2; exit 1; }
alina_service="$PREFIX/var/service/alina"
[ ! -e "$alina_service" ] || { echo 'An alina service already exists; leaving it unchanged.' >&2; exit 1; }
alina_stage=$(mktemp -d "$PREFIX/tmp/alina-service.XXXXXXXX")
trap 'rm -rf "$alina_stage"' EXIT HUP INT TERM
mkdir -p "$alina_stage/log" "$PREFIX/var/log/sv/alina"
touch "$alina_stage/down"
# Paths are stored as data, never interpolated into shell source.
printf '%s\n' "$alina_binary" > "$alina_stage/binary"
printf '%s\n' "$alina_state" > "$alina_stage/state"
printf '#!%s/bin/sh\n' "$PREFIX" > "$alina_stage/run"
cat >> "$alina_stage/run" <<'SCRIPT'
set -eu
ALINA_HOME=$(cat ./state)
export ALINA_HOME
exec "$(cat ./binary)" serve --foreground 2>&1
SCRIPT
printf '#!%s/bin/sh\n' "$PREFIX" > "$alina_stage/log/run"
cat >> "$alina_stage/log/run" <<'SCRIPT'
exec svlogd -tt "$PREFIX/var/log/sv/alina"
SCRIPT
chmod 700 "$alina_stage/run" "$alina_stage/log/run"
mv "$alina_stage" "$alina_service"
trap - EXIT HUP INT TERM
echo 'Installed, disabled. Start with: sv up alina'
