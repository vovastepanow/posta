#!/bin/sh
# Возвращает файлам проекта права, нужные Apache и парсеру (группа www-data).
# Новые файлы, которые записывает Claude, создаются с правами только для владельца —
# скрипт делает их доступными группе. Безопасно запускать сколько угодно раз, например из cron:
#   * * * * * /bin/sh /var/www/html/posta/tools/fix-permissions.sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

# Файлы, которые группа не может прочитать, и папки, в которые она не может зайти.
find "$ROOT" -path "$ROOT/.git" -prune -o -type f ! -perm -g=r ! -name config.local.php -exec chmod 664 {} + 
find "$ROOT" -path "$ROOT/.git" -prune -o -type d ! -perm -g=rx -exec chmod 2775 {} +
# Секреты VK: читают только владелец и группа www-data.
[ -f "$ROOT/config.local.php" ] && chmod 640 "$ROOT/config.local.php"
exit 0
