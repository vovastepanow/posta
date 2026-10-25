# Задание: amuzer → Keep or Skip (переименование, новый дизайн, просмотрщик «по одному»)

Ты работаешь с сайтом **amuzer** — лентой лучшего контента из Posta. Его нужно:

1. **Полностью переименовать** в **Keep or Skip** (папки, файлы, база, константы, функции, куки, конфиги Apache, тексты).
2. Сделать **новый дизайн** под выбранный логотип.
3. Заменить бесконечную ленту на **просмотрщик по одному посту**: показываем один пост и ждём реакции keep/skip. Следующий пост появляется только после реакции, и это проверяет сервер.

Сначала прочитай `README.md` сайта и весь код в `/var/www/html/amuzer` (`index.php`, `app.js`, `app.css`, `app/*.php`, `app/tools/*`, `deploy/*`). Защита от копирования (PoW, одноразовые токены, перемешанные плитки, canvas, HLS с AES, лимиты, ловушка, водяной знак) **остаётся вся**. Ломать её нельзя.

---

## 0. Что сейчас есть (проверено 2026-10-23)

| Что | Где |
|---|---|
| Код сайта | `/var/www/html/amuzer` (v0v4:www-data, setgid 2775) |
| Данные | `/var/lib/amuzer/`: `amuzer.sqlite`, `amuzer.log`, `import.lock`, `media/`, `screenshots/` |
| git | `/var/www/html/amuzer/.git` — это файл `gitdir: /var/lib/amuzer/amuzer.git`, коммитов ещё нет |
| Apache | `/etc/apache2/conf-available/amuzer.conf`, включён (`conf-enabled/amuzer.conf`); копия — `deploy/apache-local.conf` |
| Боевой vhost (не включён) | `deploy/amuzer.com.conf` |
| cron | **не установлен** (в `crontab -l` только строка Posta); образец — `deploy/cron.txt` |
| Константы | `app/config.php`: `AMUZER_DATA`, `AMUZER_MEDIA`, `AMUZER_DB`, `SITE_NAME='amuzer'`, `WATERMARK_TEXT='amuzer.com · powered by Postå™'`, `ABUSE_EMAIL='abuse@amuzer.com'` |
| Прочее | `define('AMUZER', 1)` в `index.php` и проверка `defined('AMUZER')` во всех `app/*.php`; префикс функций `amz_`; кука `amz`; соль `amuzer-map-v1` (PHP `app/media.php` и JS `MAP_SALT` в `app.js`); User-Agent `amuzer-import`; `error_log('amuzer: …')` |

В Posta (`/var/www/html/posta`) слово amuzer есть только в `*_PROMPT.md`. Код Posta **не трогать**.

---

## 1. Имена

| Было | Стало |
|---|---|
| amuzer (название на сайте) | **Keep or Skip** (в тексте) / **keep/skip** (логотип) |
| slug в путях и файлах | `keeporskip` |
| `/var/www/html/amuzer` | `/var/www/html/keeporskip` |
| `/var/lib/amuzer` | `/var/lib/keeporskip` |
| `amuzer.sqlite`, `amuzer.log`, `amuzer.git` | `keeporskip.sqlite`, `keeporskip.log`, `keeporskip.git` |
| `AMUZER`, `AMUZER_DATA`, `AMUZER_MEDIA`, `AMUZER_DB` | `KOS`, `KOS_DATA`, `KOS_MEDIA`, `KOS_DB` |
| префикс функций `amz_` | `kos_` |
| кука `amz` | `kos` (старые сессии пропадут — это нормально) |
| `amuzer-map-v1` | `kos-map-v1` (**одновременно** в PHP и JS) |
| `amuzer.conf`, `amuzer.com.conf` | `keeporskip.conf`, `keeporskip.com.conf` |
| домен `amuzer.com` | `keeporskip.com` (это предположение: держи домен в **одной** константе `SITE_DOMAIN` в `app/config.php`, чтобы потом поменять одной строкой) |
| `abuse@amuzer.com` | `abuse@` + `SITE_DOMAIN` |
| `WATERMARK_TEXT` | `SITE_DOMAIN . ' · powered by Postå™'` |
| Apache-логи `amuzer-error.log`, `amuzer-access.log` | `keeporskip-error.log`, `keeporskip-access.log` |

После переименования `grep -rni amuzer /var/www/html/keeporskip /var/lib/keeporskip --exclude-dir=media` должен ничего не находить, кроме строки в README о том, что раньше сайт назывался amuzer.

---

## 2. sudo

Части шагов нужен sudo (Apache, `/var/www/html`, `/var/lib`). **Пароль в этот файл не записан.** Попроси его у пользователя в чате один раз и используй как `echo '<пароль>' | sudo -S …`. Не записывай пароль в файлы, скрипты, логи, README или сообщения коммитов.

---

## 3. Порядок переименования

Делай по шагам и проверяй каждый.

1. **Остановить импорт.** cron не установлен, но проверь, что `import.php` сейчас не запущен: `pgrep -af import.php`.
2. **Переместить папки** с сохранением владельца, группы и setgid:
   ```
   sudo mv /var/www/html/amuzer /var/www/html/keeporskip
   sudo mv /var/lib/amuzer /var/lib/keeporskip
   cd /var/lib/keeporskip && mv amuzer.sqlite keeporskip.sqlite && mv amuzer.log keeporskip.log && mv amuzer.git keeporskip.git
   ```
   Если рядом с базой есть `amuzer.sqlite-wal` и `-shm`, переименуй их тоже. Лучше сначала сделать checkpoint: `sqlite3 amuzer.sqlite 'PRAGMA wal_checkpoint(TRUNCATE);'`.
   Исправь `/var/www/html/keeporskip/.git`: `gitdir: /var/lib/keeporskip/keeporskip.git`.
   Проверь: `ls -ld` обеих папок → `v0v4 www-data drwxrwsr-x`.
3. **Код:** всё из таблицы раздела 1. Комментарии и README тоже переписать на новое имя.
4. **Apache** (сначала новый конфиг, потом убрать старый, чтобы `.htaccess` не остался без `AllowOverride` — на Posta так уже было, база скачивалась):
   ```
   sudo cp /var/www/html/keeporskip/deploy/apache-local.conf /etc/apache2/conf-available/keeporskip.conf
   sudo a2enconf keeporskip && sudo a2disconf amuzer && sudo rm /etc/apache2/conf-available/amuzer.conf
   sudo apachectl configtest && sudo systemctl reload apache2
   ```
5. **Водяной знак вшит в пиксели** уже скопированных картинок и видео, и там написано `amuzer.com`. Медиа нужно пересобрать:
   - сделай резервную копию: `cp -a /var/lib/keeporskip /var/lib/keeporskip.bak-$(date +%F)`;
   - очисти `items`, `files`, `skipped` и папку `media/`. **`stoplist` не трогай**: это жалобы правообладателей;
   - запусти `php app/tools/import.php` заново.
   Posta чистит свой кэш, поэтому часть старых постов может не вернуться. Сообщи пользователю, сколько записей было и сколько стало.
6. **cron** (раньше не был установлен — установи с новыми путями) — обнови `deploy/cron.txt` и добавь в `crontab -e` пользователя v0v4:
   ```
   0 * * * * /usr/bin/php /var/www/html/keeporskip/app/tools/import.php >/dev/null 2>&1
   30 4 * * * /usr/bin/php /var/www/html/keeporskip/app/tools/cleanup.php >/dev/null 2>&1
   ```
   Строку Posta (`fix-permissions.sh`) не трогай.
7. **Проверка закрытости** (обязательно, код ответа каждой команды выписать в отчёт):
   ```
   curl -s -o /dev/null -w '%{http_code}\n' http://localhost/keeporskip/            # 200
   curl -s -o /dev/null -w '%{http_code}\n' http://localhost/keeporskip/app/config.php   # 404
   curl -s -o /dev/null -w '%{http_code}\n' http://localhost/keeporskip/deploy/cron.txt  # 404 или 403
   curl -s -o /dev/null -w '%{http_code}\n' http://localhost/keeporskip/.git/HEAD        # 404 или 403
   curl -s -o /dev/null -w '%{http_code}\n' http://localhost/keeporskip/README.md        # 403
   curl -s -o /dev/null -w '%{http_code}\n' http://localhost/amuzer/                     # 404
   ```
8. **Тест на кражу:** переименуй `app/tools/steal_test.sh` под новые пути и прогони его. Он должен проходить так же, как раньше.

---

## 4. Новый дизайн

### Логотип (утверждён пользователем)

Надпись **`keep/skip`** без значка:

- шрифт **Knewave** (кисть), строчные буквы, весь логотип повёрнут на **−5°**;
- буквы белые `#FFFFFF`, слэш кислотно-жёлтый `#E6FF2E`;
- фон-подложка — насыщенный синий `#2442D6`.

Шрифт держи на своём сервере: CSP разрешает только `'self'`, Google Fonts подключать нельзя.

- Скачай `Knewave-Regular.ttf` и `OFL.txt` из `https://raw.githubusercontent.com/google/fonts/main/ofl/knewave/` в `vendor/fonts/`. Лицензия OFL, файл лицензии лежит рядом.
- Подключи через `@font-face` в `app.css` с `font-display: swap`.
- Добавь `font-src 'self'` в CSP (`index.php`).
- В `.htaccess` разреши отдавать `Knewave-Regular.ttf`. Сейчас там белый список имён файлов, всё остальное закрыто.

**Фавикон и иконки** (сейчас `<link rel="icon" href="data:,">`): сгенерируй PHP-скриптом через GD (`imagettftext` с тем же TTF) PNG 32×32, 180×180 (apple-touch-icon) и 512×512. На синем `#2442D6` со скруглением — белое `k/s`, жёлтый слэш, наклон −5°. Положи в `vendor/` и разреши в `.htaccess`. Скрипт генерации — в `app/tools/`.

### Палитра и стиль: дерзко, по-уличному

| Токен | Значение | Где |
|---|---|---|
| `--bg` | `#111111` | фон страницы |
| `--brand` | `#2442D6` | шапка, плашка логотипа, фокус |
| `--acid` | `#E6FF2E` | keep, акценты, слэш |
| `--hot` | `#E8261C` | skip |
| `--text` | `#F2F2F2` | |
| `--muted` | `#A0A0A0` | подписи (проверь контраст ≥ 4.5:1 на `--bg`) |

Основной текст — системный sans. Knewave только для логотипа и крупных слов KEEP/SKIP на кнопках. Без градиентов. Углы карточки 16px, кнопки — крупные плашки.

Удали старую тему (жёлтый `#ffd23f` и прочее).

**Эффект из логотипа — для анимации реакции:** при keep пост уезжает с размытием в сторону keep, при skip — в сторону skip. Размытие сделай многослойным полупрозрачным `text-shadow`/`filter: blur` со сдвигом и RGB-сдвигом на 150–200 мс (вариант «глитч»). Без мерцаний дольше 300 мс. При `prefers-reduced-motion: reduce` — простое затухание.

---

## 5. Просмотрщик «по одному» (главная функциональная правка)

### Поведение

- На экране **один пост** во весь экран (на телефоне 100dvh; на десктопе по центру, ширина до 560px). Сверху шапка с логотипом и «powered by Postå™», снизу две большие кнопки: **SKIP** (слева, `--hot`) и **KEEP** (справа, `--acid`, тёмный текст).
- Следующий пост появляется **только после реакции**. Прокрутки ленты, «загрузить ещё» и `IntersectionObserver` для подгрузки больше нет.
- Способы ответить:
  - кнопки;
  - клавиатура: `←` / `A` — skip, `→` / `D` — keep;
  - свайп всей карточки по горизонтали: порог 100px или быстрый флик, карточка наклоняется за пальцем, у края проступает надпись KEEP/SKIP.
- Кнопки — настоящие `<button>` с `aria-label`, область нажатия ≥ 56px. После перехода фокус ставится на новую карточку.
- **Альбомы** (до 6 картинок): листаются тапом по левой/правой трети медиа и точками-индикаторами. Горизонтальный свайп — это реакция, поэтому для листания альбома его не используй.
- **Видео:** запускается без звука, когда карточка показана. Кнопки звука и паузы остаются. Реагировать можно, не досмотрев.
- **Длинный текст** прокручивается внутри карточки. Высота медиа ограничена так, чтобы кнопки всегда были видны.
- Пока ответ сервера не пришёл, кнопки заблокированы. Двойное нажатие не должно отправить две реакции.
- Чтобы после ответа не ждать, **следующий пост готовится заранее**: сервер отдаёт текущий и следующий пост, клиент заранее собирает картинки следующего на скрытом canvas. Срок жизни токена 120 с и не больше 3 использований: если следующий пост пролежал дольше ~100 с, клиент запрашивает его заново.
- Пост закончились (весь каталог пройден) — показать экран «Ты всё посмотрел, новые посты появятся через час» и **не** начинать круг заново. Если реакций нет, сервер ещё может отдавать по кругу как раньше: реши сам и опиши в README.

### Сервер (это и есть «ждать реакции»: проверка на сервере, не только в JS)

- Новая таблица (подними `SCHEMA_VERSION` до 2, миграция в `app/db.php` без потери данных):
  ```sql
  CREATE TABLE IF NOT EXISTS reactions (
    session_id TEXT NOT NULL,
    item_id    INTEGER NOT NULL,
    value      INTEGER NOT NULL,          -- 1 = keep, -1 = skip
    at         INTEGER NOT NULL,
    PRIMARY KEY (session_id, item_id)
  );
  CREATE INDEX IF NOT EXISTS reactions_item ON reactions(item_id, value);
  ```
  В `sessions` добавь колонку `pos INTEGER NOT NULL DEFAULT 0` — позиция в порядке показа (`kos_feed_order`). Клиентский `cursor` больше не нужен.
- `GET ?a=next` → `{current, next, done}`: пост на позиции `pos` и пост на `pos+1` (оба с токенами медиа, как сейчас в `kos_feed_media`). Повторный вызов без реакции возвращает **тот же** `current`, но с новыми токенами: можно перезагрузить страницу. Дальше `pos+1` выдать нельзя.
- `POST ?a=react` с телом `{"id": <id поста>, "v": "keep"|"skip"}`. Сервер проверяет:
  - `id` совпадает с постом на текущем `pos`, иначе 409 и клиент делает `next` заново;
  - прошло не меньше `REACT_MIN_INTERVAL = 0.6` с с прошлой реакции, иначе 429 и штраф по существующей логике лимитов.
  
  Затем записывает реакцию, делает `pos++` и сразу отвечает тем же `{current, next, done}`, что и `next`, чтобы не тратить второй запрос.
- Обе ручки проходят через существующий `kos_guard`: сессия, same-origin, бан, лимиты. Старый `?a=feed` удали вместе с `FEED_PAGE`, `FEED_MIN_INTERVAL`, `FEED_MAX_PAGES_HOUR`. Вместо них — `REACT_MAX_PER_HOUR` (например, 600).
- Реакции на пост, удалённый по жалобе: `remove.php` и `cleanup.php` удаляют и строки из `reactions`.
- `app/tools/stats.php`: топ-20 по keep, топ-20 по skip, доля keep у каждой темы (`topic`). Только CLI.

### Защита

Реакция через POST и запрет получать посты дальше `pos+1` дополнительно мешают выкачке: скрипту придётся слать реакцию на каждый пост и укладываться в лимит. Обнови раздел «Что защищено» в README. Ловушку-ссылку и прозрачный слой `.shield` над медиа сохрани в новой карточке.

---

## 6. Как проверять

- Тестируй в headless Chrome/Chromium на 390×844 (телефон) и 1440×900. Минимальное окно headless — 500px, поэтому телефон показывай через iframe шириной 390. Сохрани скриншоты в `/var/lib/keeporskip/screenshots/` (`mobile.png`, `desktop.png`, `react-anim.png`).
- Проверь вручную `curl` с кукой сессии и PoW (`app/tools/solve_pow.php`):
  - `react` с чужим `id` → 409;
  - две реакции быстрее 0,6 с → 429;
  - после `next` без реакции следующего поста не получить.
- `php -l` на всех PHP-файлах, `node --check app.js`.
- Не прошло — так и напиши в отчёте, с выводом команды.

## 7. Отчёт пользователю (по-русски, коротко)

- что переименовано и где теперь лежит;
- коды `curl` из шага 3.7;
- сколько постов было до пересборки медиа и сколько стало;
- как выглядит (скриншоты);
- что осталось сделать руками: домен, DNS, certbot, `deploy/keeporskip.com.conf`, настоящий `ABUSE_EMAIL`.

Закоммить в git сайта (`/var/lib/keeporskip/keeporskip.git`) одним коммитом на русском. В Posta ничего не коммить.
