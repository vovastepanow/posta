#!/usr/bin/env bash
# Called only by the locked auto-updater. Return 2 for an unhealthy or
# unrecordable deployment, 3 for inconsistent recovery state.
confirm_pending_migration() {
    local current="$1" pending_file="$2" deployed_file="$3" pending
    pending="$(cat "$pending_file")" || return 3
    [[ "$pending" =~ ^[0-9a-f]{40}$ && "$pending" = "$current" ]] || return 3
    migration_healthy || return 2
    printf '%s\n' "$pending" > "$deployed_file" || return 2
    rm -f "$pending_file" || return 2
}

# Linux systemd service runs as the deployment owner. Comparing inodes
# detects an old process that is still listening after atomic replacement.
deployment_process_matches() {
    local pid="$1" expected="$2"
    [[ "$pid" =~ ^[1-9][0-9]*$ && "/proc/$pid/exe" -ef "$expected" ]]
}
