#!/usr/bin/env bash
# Poll GitHub for a CI-green main and deploy it on the Posta host.
# Run as the owner of /var/www/html/posta, never as root.
set -Eeuo pipefail

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
git fetch --quiet origin main
current="$(git rev-parse HEAD)"
main_tip="$(git rev-parse refs/remotes/origin/main)"
deployed_file="${HOME}/.posta-auto-update-deployed"
deployed="$(cat "$deployed_file" 2>/dev/null || true)"

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

# Migrations must still be reviewed manually before advancing.
if [ -n "$deployed" ] && git cat-file -e "$deployed^{commit}" 2>/dev/null; then
    baseline="$deployed"
else
    baseline="$current"
fi
if git diff --name-only "$baseline" "$target" | grep -q '^internal/db/migrations/'; then
    fail "Database migration changed; deploy manually after backup."
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
go build -p 2 -o bin/posta.new ./cmd/posta

# Preserve last executable before swapping, then restore if restart fails.
cp -p bin/posta bin/posta.previous
mv -f bin/posta.new bin/posta
if ! sudo -n /usr/bin/systemctl restart posta ||
   ! /usr/bin/systemctl is-active --quiet posta; then
    log "Service restart/health failed; restoring previous executable."
    cp -p bin/posta.previous bin/posta
    sudo -n /usr/bin/systemctl restart posta || true
    fail "Deployment failed; inspect journalctl -u posta."
fi
printf "%s\n" "$target" > "$deployed_file"
log "Deployed ${target:0:8}; service active."
