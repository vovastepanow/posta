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
git fetch --quiet origin main
current="$(git rev-parse HEAD)"
target="$(git rev-parse refs/remotes/origin/main)"
deployed_file="${HOME}/.posta-auto-update-deployed"
deployed="$(cat "$deployed_file" 2>/dev/null || true)"
if [ "$deployed" = "$target" ]; then exit 0; fi
git merge-base --is-ancestor "$current" "$target" || fail "Main cannot fast-forward."
# A first install requires manual review if migrations exist in the target
# update; subsequent installs compare from the last successfully deployed SHA.
if [ -n "$deployed" ] && git cat-file -e "$deployed^{commit}" 2>/dev/null; then
    baseline="$deployed"
else
    baseline="$current"
fi
if git diff --name-only "$baseline" "$target" | grep -q '^internal/db/migrations/'; then
    fail "Database migration changed; deploy manually after backup."
fi

# GitHub Actions runs on hosted infrastructure. Only deploy a successful
# push run for the exact commit, never just the latest successful run.
# Exact commit SHA is embedded as the API filter. Restrict the match to the
# workflow run object and require a successful completed push on main.
# GitHub's per_page=1 selects the latest run for this precise SHA.
api_url="https://api.github.com/repos/$REPO/actions/workflows/go-tests.yml/runs?head_sha=$target&branch=main&event=push&per_page=1"
ci_json="$(curl --silent --show-error --fail --max-time 15 \
    -H 'Accept: application/vnd.github+json' \
    -H 'User-Agent: posta-auto-update' "$api_url")" || {
    log "GitHub CI unavailable; retry on next schedule."
    exit 0
}
# No Python or jq required: the SHA-filtered response must contain precisely
# the expected SHA, completed status and successful conclusion.
if ! printf '%s' "$ci_json" | grep -Fq "\"head_sha\":\"$target\"" ||
   ! printf '%s' "$ci_json" | grep -Eq '"status"[[:space:]]*:[[:space:]]*"completed"' ||
   ! printf '%s' "$ci_json" | grep -Eq '"conclusion"[[:space:]]*:[[:space:]]*"success"'; then
    log "CI for target commit not green; retry on next schedule."
    exit 0
fi

# The /tmp tmpfs on this host can fill up; both Go and cgo/GCC must use disk.
export PATH="/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin:$PATH"
export TMPDIR="${HOME}/posta-build-tmp"
export GOTMPDIR="$TMPDIR"
export GOCACHE="${HOME}/.cache/go-build"
mkdir -p "$TMPDIR" "$GOCACHE"

log "Deploying ${current:0:8} -> ${target:0:8}"
git pull --ff-only --quiet origin main
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
