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

deployment_process_matches "$$" "/proc/$$/exe"
if deployment_process_matches "$$" "$deployed" || deployment_process_matches 0 "/proc/$$/exe" ||
   deployment_process_matches "../self" "/proc/$$/exe"; then
    echo "unrelated or invalid process accepted" >&2
    exit 1
fi

# Cross-UID deployment: /proc may be inaccessible. Reject equal-second
# starts, a stale build, and any differently configured executable.
legacy_deployment_matches '{ path=/srv/posta/bin/posta ; argv[]=/srv/posta/bin/posta serve ; }' /srv/posta/bin/posta 102 101 "$current" "$current"
if legacy_deployment_matches '{ path=/srv/posta/bin/posta ; }' /srv/posta/bin/posta 101 101 "$current" "$current" ||
   legacy_deployment_matches '{ path=/srv/posta/bin/posta ; }' /srv/posta/bin/posta 102 101 "$old" "$current" ||
   legacy_deployment_matches '{ path=/other/posta ; }' /srv/posta/bin/posta 102 101 "$current" "$current"; then
    echo "unverified legacy deployment accepted" >&2
    exit 1
fi
