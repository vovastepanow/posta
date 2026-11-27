#!/usr/bin/env bash
# Poll GitHub for a CI-green main and deploy it on the Posta host.
# Run as the owner of /var/www/html/posta, never as root.
set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
REPO="vovastepanow/posta"
LOG_PREFIX="[posta-auto-update]"

log() { printf '%s %s\n' "$LOG_PREFIX" "$*"; }
fail() { log "ERROR: $*" >&2; exit 1; }

[ "$(id -u)" -ne 0 ] || fail "Do not run as root."
command -v git >/dev/null || fail "git is required"
command -v curl >/dev/null || fail "curl is required"
command -v grep >/dev/null || fail "grep is required"
export PATH="/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin:$PATH"
command -v go >/dev/null || fail "go is required (add Go to PATH for cron)"
command -v flock >/dev/null || fail "flock is required"
[ -d "$APP/.git" ] || fail "Not a git checkout: $APP"
cd "$APP"

# Prevent concurrent timer/manual deployments.
exec 9>"${HOME}/.posta-auto-update.lock"
flock -n 9 || { log "Another deployment is running; skip."; exit 0; }

[ "$(git remote get-url origin)" = "https://github.com/$REPO.git" ] ||
[ "$(git remote get-url origin)" = "https://github.com/$REPO" ] ||
[ "$(git remote get-url origin)" = "git@github.com:$REPO.git" ] ||
fail "Unexpected origin URL. Check it manually."

[ "$(git branch --show-current)" = main ] || fail "Expected branch main."
git diff --quiet && git diff --cached --quiet || fail "Tracked local changes present; refusing to overwrite."

# Research workloads are manual-only. Never launch training or benchmarking
# from the production auto-updater; keep it strictly for CI-green deploys.

# Reconcile the installed schedule as well as the repository version. The
# first deploy of this change is still started by the old 10-minute cron;
# its next invocation picks up this code and switches the host to 3 minutes.
# Do not block deployments if crontab is temporarily unavailable.
if command -v crontab >/dev/null 2>&1; then
    desired_line="*/3 * * * * /bin/bash $APP/deploy/auto-update.sh >> $HOME/posta-auto-update.log 2>&1"
    if ! crontab -l 2>/dev/null | grep -Fxq -- "$desired_line"; then
        if /bin/bash "$APP/deploy/enable-auto-update.sh"; then
            log "Cron schedule reconciled to every 3 minutes."
        else
            log "WARNING: could not update cron schedule; will retry."
        fi
    fi
fi
current="$(git rev-parse HEAD)"
deployed_file="${HOME}/.posta-auto-update-deployed"
deployed="$(cat "$deployed_file" 2>/dev/null || true)"

# Resolve pending migrations before contacting GitHub or selecting another
# green release. A newer CI commit must never overwrite an unconfirmed
# migration's recovery state or executable.
migration_pending="${HOME}/.posta-auto-update-migration-pending"
health_url="${POSTA_UPDATE_HEALTH_URL:-http://127.0.0.1:8090/}"
health_target="$current"
# Source the matching helper beside this script, including a one-shot
# bootstrap fetched from a verified CI commit into a temporary directory.
source "$SCRIPT_DIR/auto-update-state.sh"
migration_healthy() {
    local pid after headers code revision
    /usr/bin/systemctl is-active --quiet posta || return 1
    pid="$(/usr/bin/systemctl show --property=MainPID --value posta)" || return 1
    [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
    headers="$(curl --silent --output /dev/null --dump-header - --max-time 3 --write-out '\n%{http_code}' "$health_url")" || return 1
    code="$(printf '%s\n' "$headers" | tail -n 1)"
    [[ "$code" =~ ^(2|3)[0-9][0-9]$ || "$code" = 401 || "$code" = 403 ]] || return 1
    revision="$(printf '%s\n' "$headers" | awk 'tolower($1)=="x-posta-revision:" {gsub(/\r/,"",$2); print $2}')"
    after="$(/usr/bin/systemctl show --property=MainPID --value posta)" || return 1
    [ "$pid" = "$after" ] || return 1
    if [ -n "$revision" ]; then
        [ "$revision" = "$health_target" ]
        return
    fi
    # Older builds have no revision header. Reading another UID's /proc
    # exe is often forbidden. In that case use systemd's exact ExecStart,
    # installed binary build metadata and a strictly newer start timestamp.
    local executable started changed binary_revision dbus_start
    binary_revision="$(go version -m "$APP/bin/posta" | awk '$1=="build" && $2 ~ /^vcs.revision=/ {sub(/^vcs.revision=/,"",$2);print $2}')" || return 1
    [ "$binary_revision" = "$health_target" ] || return 1
    if deployment_process_matches "$pid" "$APP/bin/posta"; then
        [ "$pid" = "$(/usr/bin/systemctl show --property=MainPID --value posta)" ]
        return
    fi
    executable="$(/usr/bin/systemctl show --property=ExecStart --value posta)" || return 1
    # Prefer systemd's microsecond D-Bus property: the binary replacement and
    # service start usually occur within the same wall-clock second.
    dbus_start="$(busctl --system get-property org.freedesktop.systemd1 /org/freedesktop/systemd1/unit/posta_2eservice org.freedesktop.systemd1.Service ExecMainStartTimestamp 2>/dev/null || true)"
    if [[ "$dbus_start" =~ ^t\ ([0-9]+)$ ]]; then
        started="${BASH_REMATCH[1]}"
        changed="$(date -d "$(stat -c %z "$APP/bin/posta")" +%s%6N 2>/dev/null)" || return 1
    else
        started="$(/usr/bin/systemctl show --property=ExecMainStartTimestamp --value posta)" || return 1
        started="$(date -d "$started" +%s 2>/dev/null)" || return 1
        changed="$(stat -c %Z "$APP/bin/posta")" || return 1
    fi
    legacy_deployment_matches "$executable" "$APP/bin/posta" "$started" "$changed" "$binary_revision" "$health_target" || return 1
    [ "$pid" = "$(/usr/bin/systemctl show --property=MainPID --value posta)" ]
}
if [ -e "$migration_pending" ]; then
    if confirm_pending_migration "$current" "$migration_pending" "$deployed_file"; then
        log "Deployed ${current:0:8}; service healthy after migrations."
        deployed="$current"
    else
        pending_result=$?
        if [ "$pending_result" = 3 ]; then
            fail "Pending migration marker does not match the checkout; manual recovery required."
        fi
        fail "Migration deployment not yet healthy; previous executable and database backup preserved. Inspect service journal."
    fi
fi

git fetch --quiet origin main
main_tip="$(git rev-parse refs/remotes/origin/main)"

# No GitHub API request is necessary when the fetched tip is already deployed.
if [ "$current" = "$main_tip" ] && [ "$deployed" = "$main_tip" ]; then exit 0; fi

# The newest commit is often still running CI, especially during rapid
# development. Deploy the newest successful main-branch commit instead of
# indefinitely skipping releases while main keeps advancing.
api_path="repos/$REPO/actions/workflows/go-tests.yml/runs?branch=main&event=push&status=success&per_page=1"
# Reuse the deployment user's GitHub CLI login or GH_TOKEN. Cron runs
# under the same HOME; do not print tokens or store them in this repository.
if command -v gh >/dev/null && GH_PROMPT_DISABLED=1 gh auth token --hostname github.com >/dev/null 2>&1; then
    ci_json="$(GH_PROMPT_DISABLED=1 timeout 20s gh api --hostname github.com "$api_path")" || {
        log "Authenticated GitHub CI query failed; check gh auth status."
        exit 0
    }
else
    ci_json="$(curl --silent --show-error --fail --max-time 15 \
        -H 'Accept: application/vnd.github+json' \
        -H 'User-Agent: posta-auto-update' "https://api.github.com/$api_path")" || {
        log "Anonymous GitHub CI query failed; configure gh auth login for this user."
        exit 0
    }
fi
target="$(printf '%s' "$ci_json" | grep -oE '"head_sha"[[:space:]]*:[[:space:]]*"[0-9a-f]{40}"' | head -n 1 | grep -oE '[0-9a-f]{40}' || true)"
if [ -z "$target" ] || ! printf '%s' "$ci_json" | grep -Eq '"conclusion"[[:space:]]*:[[:space:]]*"success"'; then
    log "No successful CI run for main; retry."
    exit 0
fi
git cat-file -e "$target^{commit}" 2>/dev/null || fail "Green CI commit not available in fetched main."
git merge-base --is-ancestor "$target" "$main_tip" || fail "Green CI commit is not an ancestor of main."
if [ "$current" = "$target" ] && [ "$deployed" = "$target" ]; then exit 0; fi
if ! git merge-base --is-ancestor "$current" "$target"; then
    log "Current checkout is ahead of latest green CI; waiting for newer tests."
    exit 0
fi

# Only append-only migration files may deploy automatically. The service
# itself creates a PostgreSQL backup before applying pending migrations.
if [ -n "$deployed" ] && git cat-file -e "$deployed^{commit}" 2>/dev/null; then
    baseline="$deployed"
else
    baseline="$current"
fi
migration_release=0
migration_changes="$(git diff --no-renames --name-status "$baseline" "$target" -- internal/db/migrations/)"
if [ -n "$migration_changes" ]; then
    if printf '%s\n' "$migration_changes" | grep -qv '^A[[:space:]]'; then
        fail "Existing database migration modified or deleted; manual review required."
    fi
    migration_release=1
    log "New migrations detected; service will back up PostgreSQL before applying."
fi



# The /tmp tmpfs on this host can fill up; both Go and cgo/GCC must use disk.
export PATH="/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin:$PATH"
export TMPDIR="${HOME}/posta-build-tmp"
export GOTMPDIR="$TMPDIR"
export GOCACHE="${HOME}/.cache/go-build"
mkdir -p "$TMPDIR" "$GOCACHE"

log "Deploying ${current:0:8} -> ${target:0:8}"
git merge --ff-only --quiet "$target"
[ "$(git rev-parse HEAD)" = "$target" ] || fail "Working copy changed during pull."
go build -buildvcs=true -p 2 -o bin/posta.new ./cmd/posta
health_target="$target"

# Preserve last executable; only non-migration releases may auto-roll back.
if [ ! -f "$migration_pending" ]; then
    cp -p bin/posta bin/posta.previous
fi
mv -f bin/posta.new bin/posta
if [ "$migration_release" = 1 ]; then
    printf '%s\n' "$target" > "$migration_pending"
    if ! sudo -n /usr/bin/systemctl restart posta; then
        fail "Migration release restart failed; not rolling back across an unknown database schema."
    fi
    # Type=simple systemd services become active before database setup ends.
    # Wait for the HTTP listener, not merely a running process.
    for ((attempt=0; attempt<120; attempt++)); do
        if migration_healthy; then
            printf '%s\n' "$target" > "$deployed_file"
            rm -f "$migration_pending"
            log "Deployed ${target:0:8}; service healthy after migrations."
            exit 0
        fi
        sleep 5
    done
    fail "Migration release did not become healthy; database backup and previous executable preserved."
fi
if sudo -n /usr/bin/systemctl restart posta; then
    # Type=simple can be active while startup is still failing. Confirm
    # both the new executable and HTTP readiness before recording a deploy.
    for ((attempt=0; attempt<30; attempt++)); do
        if migration_healthy; then
            printf '%s\n' "$target" > "$deployed_file"
            log "Deployed ${target:0:8}; service healthy."
            exit 0
        fi
        sleep 2
    done
fi
log "Service restart/health failed; restoring previous executable."
# Never truncate a running executable (ETXTBSY). Replace it atomically.
cp -p bin/posta.previous bin/posta.rollback
mv -f bin/posta.rollback bin/posta
sudo -n /usr/bin/systemctl restart posta || true
fail "Deployment failed; inspect journalctl -u posta."
