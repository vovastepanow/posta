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

# When the service and updater share a UID, inode comparison is reliable.
# When they do not, /proc/PID/exe can be inaccessible.
deployment_process_matches() {
    local pid="$1" expected="$2"
    [[ "$pid" =~ ^[1-9][0-9]*$ && "/proc/$pid/exe" -ef "$expected" ]]
}

# Conservative compatibility check for releases without an HTTP build header.
# ExecStart's path is supplied by systemd, not by an HTTP client. Require a
# strictly later process start than replacement of the exact built binary.
legacy_deployment_matches() {
    local exec_start="$1" expected="$2" started="$3" changed="$4" revision="$5" target="$6"
    [[ "$exec_start" == *"path=$expected ;"* ]] || return 1
    [[ "$revision" =~ ^[0-9a-f]{40}$ && "$revision" = "$target" ]] || return 1
    [[ "$started" =~ ^[0-9]+$ && "$changed" =~ ^[0-9]+$ ]] || return 1
    (( started > changed ))
}
