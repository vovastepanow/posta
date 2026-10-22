#!/bin/sh
# Настраивает Apache для Posta (нужен root): включает чтение .htaccess и задаёт лимиты загрузки для редактора поста.
# Без этого .htaccess игнорируется (в Debian/Ubuntu для /var/www стоит AllowOverride None): по HTTP отдаются .git, истории оценок,
# каталог источников и другие служебные файлы. Запуск: sudo sh tools/apache-setup.sh
set -e
DIR="$(cd "$(dirname "$0")/.." && pwd)"
CONF=/etc/apache2/conf-available/posta.conf
cat > "$CONF" <<CONFIG
# Posta (создано tools/apache-setup.sh)
<Directory $DIR>
    AllowOverride All
    <IfModule mod_php.c>
        php_value upload_max_filesize 64M
        php_value post_max_size 400M
        php_value max_input_time 300
    </IfModule>
</Directory>
CONFIG
a2enconf posta >/dev/null
a2enmod headers alias >/dev/null 2>&1 || true
apache2ctl configtest
systemctl reload apache2
echo "Готово: Apache читает .htaccess для $DIR. Проверьте: curl -I http://localhost/$(basename "$DIR")/.git/config (должно быть 403 или 404)."
