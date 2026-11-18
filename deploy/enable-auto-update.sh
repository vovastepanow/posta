#!/usr/bin/env bash
# Install a user crontab entry for the local, CI-gated Posta updater.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
[ "$(id -u)" -ne 0 ] || { echo "Run as your normal user, not root." >&2; exit 1; }
command -v crontab >/dev/null || { echo "cron/crontab is missing; install cron before continuing." >&2; exit 1; }
[ -f "$APP/deploy/auto-update.sh" ] || { echo "Updater script missing." >&2; exit 1; }

# Preserve unrelated crontab entries; replace only our managed line.
existing="$(crontab -l 2>/dev/null || true)"
{
    printf '%s\n' "$existing" | grep -v -F "$APP/deploy/auto-update.sh" || true
    printf '*/10 * * * * /bin/bash %s/deploy/auto-update.sh >> %s/posta-auto-update.log 2>&1\n' "$APP" "$HOME"
} | sed '/^$/d' | crontab -
echo "Automatic Posta updates enabled: every 10 minutes after green GitHub CI."
echo "Log: $HOME/posta-auto-update.log"
echo "To disable: crontab -e and remove the posta auto-update line."
