#!/usr/bin/env bash
set -Eeuo pipefail
cd "$(dirname "$0")/.."
source deploy/auto-update-state.sh
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
pending="$tmp/pending"
deployed="$tmp/deployed"
old=1111111111111111111111111111111111111111
current=2222222222222222222222222222222222222222
newer=3333333333333333333333333333333333333333
printf '%s\n' "$old" > "$deployed"
printf '%s\n' "$current" > "$pending"

migration_healthy() { return 1; }
if confirm_pending_migration "$current" "$pending" "$deployed"; then
    echo "unhealthy migration incorrectly confirmed" >&2
    exit 1
else
    [ "$?" = 2 ]
fi
[ "$(cat "$deployed")" = "$old" ]
[ "$(cat "$pending")" = "$current" ]

migration_healthy() { return 0; }
if confirm_pending_migration "$newer" "$pending" "$deployed"; then
    echo "newer checkout incorrectly overwrote recovery state" >&2
    exit 1
else
    [ "$?" = 3 ]
fi
[ "$(cat "$deployed")" = "$old" ]
[ "$(cat "$pending")" = "$current" ]

confirm_pending_migration "$current" "$pending" "$deployed"
[ "$(cat "$deployed")" = "$current" ]
[ ! -e "$pending" ]
