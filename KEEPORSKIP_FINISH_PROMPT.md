# Задание: доделать Keep or Skip (продолжение KEEPORSKIP_PROMPT.md)

Полное задание — `/var/www/html/posta/KEEPORSKIP_PROMPT.md`. **Прочитай его целиком**, но выполнять с начала не надо: прошлая сессия
сделала часть шагов и оборвалась. Ниже — что уже сделано (проверено 2026-10-23) и что осталось.

## Уже сделано — не повторять

- Папки перенесены: код — `/var/www/html/keeporskip`, данные — `/var/lib/keeporskip` (`keeporskip.sqlite`, `keeporskip.log`, `keeporskip.git`, `media/`).
  Владелец и setgid правильные. `/var/www/html/keeporskip/.git` → `gitdir: /var/lib/keeporskip/keeporskip.git`. Старых `/var/www/html/amuzer`
  и `/var/lib/amuzer` нет.
- Apache: `/etc/apache2/conf-available/keeporskip.conf` включён, `amuzer.conf` удалён. `http://localhost/keeporskip/` → 200, `app/config.php` → 403, `/amuzer/` → 404.
- В `app/config.php` поменяны только пути (`/var/lib/keeporskip`, `keeporskip.sqlite`), имена констант ещё старые (`AMUZER_DATA` и т. д.).
- В базе 81 пост (`items`). Запомни это число для отчёта.

**sudo больше не нужен**: всё оставшееся делается от пользователя v0v4. Если всё же понадобится — спроси пароль в чате, никуда его не записывай.
Не перемещай папки и не трогай конфиги в `/etc`.

## Осталось — по порядку, проверяя каждый шаг

1. **Переименование в коде** (шаг 3 и таблица раздела 1 исходного задания). Сейчас `amuzer` встречается в 21 файле:
   `app/*.php`, `app/tools/*`, `index.php`, `app.js`, `app.css`, `.htaccess`, `.gitignore`, `README.md`, `deploy/*`.
   - `deploy/amuzer.com.conf` переименуй обычным `mv` в `deploy/keeporskip.com.conf` (коммитов нет, `git mv` не нужен) и поправь внутри.
   - `deploy/apache-local.conf` сделай **точной копией** включённого `/etc/apache2/conf-available/keeporskip.conf` (его можно читать без sudo).
   - Соль `amuzer-map-v1` → `kos-map-v1` **одновременно** в `app/media.php` и `MAP_SALT` в `app.js` — иначе картинки соберутся кашей.
   - Константа `SITE_DOMAIN = 'keeporskip.com'`, от неё `ABUSE_EMAIL` и `WATERMARK_TEXT`.
   - Проверка: `grep -rni amuzer /var/www/html/keeporskip /var/lib/keeporskip --exclude-dir=media --exclude-dir=keeporskip.git` — только строка в README
     «раньше назывался amuzer». `php -l` на всех PHP, `node --check app.js`, `curl http://localhost/keeporskip/` → 200 и в логе Apache
     (`/var/log/apache2/error.log` читается? если нет — `php -r` с подключением `index.php` в CLI) нет фатальных ошибок.
2. **Дизайн** — раздел 4 исходного задания целиком (Knewave в `vendor/fonts/`, CSP `font-src 'self'`, белый список `.htaccess`, фавиконы через GD,
   палитра, анимация реакции). Сеть нужна один раз — скачать шрифт и `OFL.txt`.
3. **Просмотрщик «по одному»** — раздел 5 целиком (таблица `reactions`, `sessions.pos`, `SCHEMA_VERSION = 2` с миграцией, `?a=next`, `?a=react`,
   удалить `?a=feed` и `FEED_*`, `stats.php`, клиент без бесконечной ленты).
4. **Пересборка медиа** (шаг 5): резервная копия `cp -a /var/lib/keeporskip /var/lib/keeporskip.bak-$(date +%F)`, очистить `items`, `files`, `skipped`,
   `media/` (**`stoplist` не трогать**), `php app/tools/import.php`. Делай **после** шагов 1 и 3: импорт должен писать уже новый водяной знак и
   новую схему. Записать «было 81 → стало N».
5. **cron** (шаг 6): обновить `deploy/cron.txt`, добавить две строки в `crontab` v0v4; строку Posta с `fix-permissions.sh` сохранить
   (делай через `crontab -l > файл`, дописать, `crontab файл`, затем `crontab -l` в отчёт).
6. **Проверки** (шаг 7 и 8, раздел 6): коды `curl`, `steal_test.sh` с новыми путями, проверки `react` (409 / 429 / нельзя получить пост дальше `pos+1`),
   скриншоты 390 и 1440 в `/var/lib/keeporskip/screenshots/`.
7. **Коммит** в git сайта одним коммитом на русском. В Posta ничего не коммить.

## Чтобы не оборваться снова

- После каждого пункта 1–6 коротко пиши в чат «пункт N готов: …» — если сессия прервётся, следующая продолжит с нужного места.
- Не читай целиком `vendor/hls.min.js` и файлы в `media/` — они большие и бесполезны для задачи.
- Отчёт — раздел 7 исходного задания. Что не получилось — так и напиши, с выводом команды.
