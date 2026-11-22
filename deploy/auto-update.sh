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

# Independent bounded research job: poll for a successful prior comparison.
# This is intentionally before the "already deployed" early return, so a
# completed comparison can unlock the next stage without a new code deployment.
if [ -f "$APP/research/posta_multi/host_retrieval_hook.sh" ]; then
    POSTA_DEPLOY_DIR="$APP" /bin/bash "$APP/research/posta_multi/host_retrieval_hook.sh" ||
        log "WARNING: Posta Multi v9 hook could not start."
fi

# Run only after the separate test-split comparison has completed. This hook
# is polled before the "already deployed" exit: no extra code deploy needed.
if [ -f "$APP/research/posta_multi/host_geometry_hook.sh" ]; then
    POSTA_DEPLOY_DIR="$APP" /bin/bash "$APP/research/posta_multi/host_geometry_hook.sh" ||
        log "WARNING: Posta Multi geometry diagnostic hook could not start."
fi

# CPU-only aggregate retrieval split audit after existing held-out tests.
if [ -f "$APP/research/posta_multi/host_split_audit_hook.sh" ]; then
    POSTA_DEPLOY_DIR="$APP" /bin/bash "$APP/research/posta_multi/host_split_audit_hook.sh" ||
        log "WARNING: Posta Multi split audit hook could not start."
fi

# CPU-only verdict from the historical v1/v8/v9 validation reports.
if [ -f "$APP/research/posta_multi/host_checkpoint_decision_hook.sh" ]; then
    POSTA_DEPLOY_DIR="$APP" /bin/bash "$APP/research/posta_multi/host_checkpoint_decision_hook.sh" ||
        log "WARNING: Posta Multi checkpoint decision could not run."
fi

# Full 235M Run 001: one synthetic GPU memory and optimizer feasibility step.
# A failed smoke is recorded, never retried automatically or treated as trained weights.
if [ -f "$APP/research/posta_multi/host_run001_smoke_hook.sh" ]; then
    POSTA_DEPLOY_DIR="$APP" /bin/bash "$APP/research/posta_multi/host_run001_smoke_hook.sh" ||
        log "WARNING: Posta Multi Run 001 GPU smoke hook could not start."
fi

# Run 001 data/corpus readiness: report-only, never downloads or trains.
if [ -f "$APP/research/posta_multi/host_run001_preflight_hook.sh" ]; then
    POSTA_DEPLOY_DIR="$APP" /bin/bash "$APP/research/posta_multi/host_run001_preflight_hook.sh" ||
        log "WARNING: Posta Multi Run 001 preflight hook could not start."
fi

# Second Run 001 synthetic probe uses real 96-token training length.
# Gate on the first successful probe; never retrain or download data.
if [ -f "$APP/research/posta_multi/host_run001_seq96_hook.sh" ]; then
    POSTA_DEPLOY_DIR="$APP" /bin/bash "$APP/research/posta_multi/host_run001_seq96_hook.sh" ||
        log "WARNING: Posta Multi Run 001 seq96 probe hook failed."
fi

# Check the Run 001 anti-collapse gradient-cache implementation on synthetic
# tokens only, gated on a successful previous full-length GPU probe.
if [ -f "$APP/research/posta_multi/host_run001_retrieval_smoke_hook.sh" ]; then
    POSTA_DEPLOY_DIR="$APP" /bin/bash "$APP/research/posta_multi/host_run001_retrieval_smoke_hook.sh" ||
        log "WARNING: Run 001 anti-collapse GPU smoke hook failed."
fi

# Read-only lexical baseline on the existing validation split; no model weights
# or database changes, and never an automatic training or promotion action.
if [ -f "$APP/research/posta_multi/host_bm25_hook.sh" ]; then
    POSTA_DEPLOY_DIR="$APP" /bin/bash "$APP/research/posta_multi/host_bm25_hook.sh" ||
        log "WARNING: Run 001 BM25 validation hook failed."
fi

# Evaluation-only RU+EN human-judged benchmark, no training or DB changes.
if [ -f "$APP/research/posta_multi/host_nanomiracl_hook.sh" ]; then
    POSTA_DEPLOY_DIR="$APP" /bin/bash "$APP/research/posta_multi/host_nanomiracl_hook.sh" ||
        log "WARNING: Run 001 NanoMIRACL benchmark hook failed."
fi

# Pin source revisions and inspect metadata/column types of selected text
# and retrieval datasets. No source rows, media or large shards are downloaded.
if [ -f "$APP/research/posta_multi/host_source_audit_hook.sh" ]; then
    POSTA_DEPLOY_DIR="$APP" /bin/bash "$APP/research/posta_multi/host_source_audit_hook.sh" ||
        log "WARNING: Run 001 external source metadata audit hook failed."
fi

# One-shot language-balanced tokenizer intake audit on the local *quarantined*
# candidate. It does not train a tokenizer or read model checkpoints.
if [ -f "$APP/research/posta_multi/host_run001_tokenizer_audit_hook.sh" ]; then
    POSTA_DEPLOY_DIR="$APP" /bin/bash "$APP/research/posta_multi/host_run001_tokenizer_audit_hook.sh" ||
        log "WARNING: Run 001 tokenizer intake audit could not start."
fi

# Aggregate-only audit of ALREADY downloaded 100GiB dataset files.
# Read-only: no new sources, no model initialization, no production DB writes.
if [ -f "$APP/research/posta_multi/host_run001_hdd_inventory_hook.sh" ]; then
    POSTA_DEPLOY_DIR="$APP" /bin/bash "$APP/research/posta_multi/host_run001_hdd_inventory_hook.sh" ||
        log "WARNING: Run 001 HDD inventory could not start."
fi

# REAL 235M training trial: use ONLY existing screened local data.
# Requires successful seq96 GPU smoke, isolated research tokenizer and
# nonproduction checkpoints. Never changes production model or DB.
if [ -f "$APP/research/posta_multi/host_run001_real_pilot_hook.sh" ]; then
    POSTA_DEPLOY_DIR="$APP" /bin/bash "$APP/research/posta_multi/host_run001_real_pilot_hook.sh" ||
        log "WARNING: Run 001 private real-data pilot could not start."
fi

# Advance the already prepared private AV queue after a successful retrieval.
# A lock and existing-output checks prevent duplicate GPU pilots.
if [ -f "$APP/research/posta_multi/host_av_queue_hook.sh" ]; then
    POSTA_DEPLOY_DIR="$APP" /bin/bash "$APP/research/posta_multi/host_av_queue_hook.sh" ||
        log "WARNING: Posta Multi AV queue hook could not start."
fi

# Once the queued AV pilot has finished, compare the frozen before/after
# held-out pools on CPU. No further training or model promotion.
if [ -f "$APP/research/posta_multi/host_av_compare_hook.sh" ]; then
    POSTA_DEPLOY_DIR="$APP" /bin/bash "$APP/research/posta_multi/host_av_compare_hook.sh" ||
        log "WARNING: Posta Multi AV comparison hook failed."
fi

# Real, resumable private 235M AV training; only after the completed pilot,
# frozen held-out comparison, idle GPU, and previous successful retrieval.
# Check on every cron tick, even when no new git commits need deployment.
if [ -f "$APP/research/posta_multi/host_av_continuation_hook.sh" ]; then
    POSTA_DEPLOY_DIR="$APP" /bin/bash "$APP/research/posta_multi/host_av_continuation_hook.sh" ||
        log "WARNING: Posta Multi sustained AV training hook failed."
fi

# AV geometry refinement is manual-only until an external-quality benchmark
# justifies more spending. Do not auto-start experimental GPU training.

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

# The newest commit is often still running CI, especially during rapid
# development. Deploy the newest successful main-branch commit instead of
# indefinitely skipping releases while main keeps advancing.
api_url="https://api.github.com/repos/$REPO/actions/workflows/go-tests.yml/runs?branch=main&event=push&status=success&per_page=1"
ci_json="$(curl --silent --show-error --fail --max-time 15 \
    -H 'Accept: application/vnd.github+json' \
    -H 'User-Agent: posta-auto-update' "$api_url")" || {
    log "GitHub CI unavailable; retry on next schedule."
    exit 0
}
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

# Read-only test-split comparison of completed retrieval experiments; no training.
if [ -f "$APP/research/posta_multi/host_holdout_hook.sh" ]; then
    POSTA_DEPLOY_DIR="$APP" /bin/bash "$APP/research/posta_multi/host_holdout_hook.sh" ||
        log "WARNING: Posta Multi held-out comparison hook could not start."
fi

# Opted-in one-shot research validation. Failure never interrupts Posta deployment.
# The helper only compares existing checkpoints and writes a report to the HDD.
if [ -f "$APP/research/posta_multi/host_compare_hook.sh" ]; then
    POSTA_DEPLOY_DIR="$APP" /bin/bash "$APP/research/posta_multi/host_compare_hook.sh" ||
        log "WARNING: Posta Multi evaluation hook could not start."
fi
