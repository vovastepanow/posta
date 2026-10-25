# Задача: amuzer.com — бесконечная лента лучшего из Posta, которую трудно утащить

Ты делаешь **новый отдельный сайт** `amuzer` в `/var/www/html/amuzer` (локально `http://localhost/amuzer/`, позже —
домен amuzer.com). Это бесконечная лента развлекательного контента. Контент — лучшее из сервиса Posta
(`/var/www/html/posta`): посты, которые пользователи Posta лайкнули или опубликовали. Под каждым постом подпись
**«powered by Postå™»** со ссылкой на лендинг Posta — сайт работает как реклама Posta.

Главное требование владельца: **утащить контент с сайта должно быть невозможно или очень муторно.** Честно:
невозможно не бывает — всё, что видно на экране, можно снять скриншотом или записью экрана. Цель — чтобы
**массовое** выкачивание (скрипт, `wget`, «Сохранить картинку», расширения-качалки, хотлинк на чужом сайте) не
работало или давало мусор, а ручное сохранение по одной картинке было неудобным и с водяным знаком.

```
Posta (только чтение)                       amuzer
 data/posta.sqlite  ──┐
 /var/lib/posta/media ┴─► tools/import.php ─► /var/lib/amuzer/{amuzer.sqlite, media/}  (вне веб-корня!)
                          (cron раз в час)        │
                                                  ▼
                       index.php ─ ?a=feed  → JSON страница ленты (по сессии, с лимитами)
                                 ─ ?a=m     → перемешанная картинка по одноразовому токену
                                 ─ ?a=key / ?a=seg → видео HLS, зашифрованное AES-128, ключ по токену
                       app.js: canvas собирает картинку из перемешанных плиток, hls.js играет видео
```

Делай части по порядку: 0 → A → B → C → D → E → F → G → H. Не начинай следующую, пока не прошла проверка предыдущей.

## Перед началом: что сделал владелец (проверь, не делай сам)

Каталоги принадлежат root, поэтому владелец заранее выполнил:
```bash
sudo install -d -o v0v4 -g www-data -m 2775 /var/www/html/amuzer /var/lib/amuzer /var/lib/amuzer/media
```
Проверь `ls -ld /var/www/html/amuzer /var/lib/amuzer/media`. Если каталогов нет — **остановись** и напиши
владельцу эту команду. Сам sudo не вызывай.

## Общие правила

- **Не делай git commit** ни в Posta, ни в амузере. В `/var/www/html/amuzer` сделай `git init` и `.gitignore`
  (без коммита), чтобы владелец мог закоммитить сам.
- **Posta — только на чтение.** Не меняй ни одного файла в `/var/www/html/posta` (кроме того, что прямо разрешено
  ниже: ничего). Базу Posta открывай только так:
  `new PDO('sqlite:file:/var/www/html/posta/data/posta.sqlite?mode=ro', null, null, [PDO::ATTR_ERRMODE => PDO::ERRMODE_EXCEPTION])`
  (если `mode=ro` через URI не сработает — `PDO::SQLITE_ATTR_OPEN_FLAGS => PDO::SQLITE_OPEN_READONLY` в PHP 8.1+).
  **Не подключай `bootstrap.php` Posta** в коде сайта амузера. В `tools/import.php` подключать его можно только ради
  `content_is_adult()` и `topic_save_denied()` (см. часть B) — и только в командной строке.
- **Никогда не открывай сайт Posta** (`http://localhost/posta/...`): каждая загрузка без куки создаёт пользователя
  Posta и запускает парсер, который нельзя остановить без sudo. **Сайт амузера** (`http://localhost/amuzer/...`)
  открывать curl'ом и headless Chrome можно и нужно — он с Posta не связан.
- **Ollama не используй.** Модели не нужны.
- **Не трогай Apache, `php.ini`, `crontab`, systemd, sudo.** Конфиги Apache и строку cron только подготовь файлами и
  покажи в отчёте (часть H).
- **Все данные амузера — вне веб-корня**, в `/var/lib/amuzer`. В `/var/www/html/amuzer` лежат только `index.php`,
  статические `app.js`, `app.css`, шрифты, `vendor/` и `.htaccess`. Причина: у Posta `.htaccess` сейчас не читается
  Apache (нет `AllowOverride`), и её база отдаётся по HTTP. У амузера утечь не должно ничего, даже если `.htaccess`
  проигнорирован. Остальной PHP (`lib/`, `tools/`) — тоже вне веб-корня или так, чтобы прямой запрос к нему ничего не
  делал: лучше всего — код в `/var/www/html/amuzer/app/` с первой строкой
  `if (PHP_SAPI !== 'cli' && !defined('AMUZER')) { http_response_code(404); exit; }`, а `index.php` делает `define('AMUZER', 1)`.
- Код: PHP 8, короткие функции, комментарии по-русски одной строкой, ошибки — `RuntimeException` с понятным текстом.
  JS — один модуль `app.js` без сборщиков. Никаких внешних CDN на странице: CSP `script-src 'self'`.
- Тексты постов — чужие данные: на странице только `textContent`.

## Часть 0. Прочитай

- В Posta (только чтение): `lib/db.php` (схема `history_items`, `published`), `lib/content.php` — `content_image_key`,
  `content_gallery`, `content_is_adult`, `media_cache_path`, `MEDIA_CACHE_DIR` и `MEDIA_CACHE_URL` в `config.php` и
  `config.local.php` (сейчас медиа в `/var/lib/posta/media`, в записях ссылка вида `media/<hash>.jpg`),
  `topic_save_denied`. Посмотри 3–4 записи `history_items` с `collection = 'liked'` (поле `data` — JSON:
  `image`, `image_local`, `video`, `video_local`, `gallery`, `text`, `topic`, `source`, `phash`, `timestamp`) и
  записи `published` (там `url` поста в канале; сами медиа ищи по `history_items` того же пользователя).
- **Важно:** Posta периодически чистит свой кэш медиа, а исходные ссылки Telegram (`cdn*.telesco.pe`) живут часы.
  Поэтому импорт **копирует** файлы к себе сразу; запись без живого файла пропускается.
- `ffmpeg -version`, `php -m | grep -i gd` — ffmpeg и GD с FreeType есть.

## Часть A. Каркас и база амузера

`/var/lib/amuzer/amuzer.sqlite` (WAL), таблицы:
- `items(id TEXT PRIMARY KEY` — случайные 16 hex, **не** порядковый номер, `kind` image|video|album, `text`, `topic`,
  `source_name`, `phash`, `width`, `height`, `created_at`, `posta_key` UNIQUE — `content_image_key` записи Posta`)`;
- `files(item_id, pos, kind, path, variants)` — `path` относительный внутри `/var/lib/amuzer/media`;
- `sessions(id, created_at, last_at, pow_ok, served, banned_until, strikes)`;
- `hits(session_id, ip_hash, at, what)` — для лимитов, чистка старше суток;
- `meta(k, v)` — версия схемы, секрет HMAC (32 байта `random_bytes`, генерируется один раз).

`app/config.php`: `AMUZER_DATA = '/var/lib/amuzer'`, `POSTA_DB`, `POSTA_MEDIA = '/var/lib/posta/media'`,
`POSTA_URL` (ссылка из подписи; по умолчанию `http://localhost/posta/landing/index-posta.html`, владелец поменяет на
домен), `SITE_NAME = 'amuzer'`.

**Проверка:** `php -l` по файлам; `php app/tools/db_init.php` создаёт базу; повторный запуск ничего не ломает.

## Часть B. Импорт из Posta

`app/tools/import.php` (только CLI, под `flock` на `/var/lib/amuzer/import.lock`):
1. Берёт записи `history_items` с `collection = 'liked'` всех пользователей + записи, опубликованные в каналы
   (сопоставь `published` с `history_items` того же `user_id`; если надёжно сопоставить нельзя — опиши в отчёте и
   бери только `liked`).
2. Отсеивает: `content_is_adult($r)` и темы, для которых `topic_save_denied($topic) !== null` (подключи
   `/var/www/html/posta/bootstrap.php` **только здесь**; если подключение даёт побочные эффекты — создаёт
   пользователя, пишет в базу Posta — **не подключай**, а перенеси логику проверки 18+ копией с комментарием,
   откуда она). Плюс жалобы: записи, которые хоть один пользователь Posta отправил в `complaints`, — мимо.
3. Дубли: одинаковый `posta_key` или `phash` с расстоянием Хэмминга ≤ 6 к уже импортированному — мимо.
4. Файл: `image_local` / `video_local` → путь в `/var/lib/posta/media` (`basename`, проверка `^[a-f0-9]{32}\.[a-z0-9]{2,5}$`);
   нет файла → попробуй исходный `image` / `video` только для `https://` и только через `curl` с таймаутом 15 с и
   лимитом 20 МБ; не вышло — запись пропускается. Альбомы (`gallery`) — до 6 файлов.
5. **Подготовка картинки** (GD), один раз при импорте:
   - уменьшить до 1080 px по длинной стороне;
   - **водяной знак** в пикселях: полупрозрачная надпись «amuzer.com · powered by Postå™» в углу + слабая
     диагональная сетка того же текста по всей картинке (прозрачность ~6–8 %, чтобы не мешала смотреть, но и
     не вырезалась кропом). Шрифт — TTF из Posta `fonts/` нельзя (woff2): положи в `app/fonts/` свободный TTF
     (например, `DejaVuSans-Bold.ttf` из `/usr/share/fonts`, если есть; проверь лицензию в отчёте);
   - **перемешивание:** разрежь на сетку 6×6, сохрани **3 варианта** с разными случайными перестановками плиток
     (`variants` = 3, перестановки — в `files` JSON-ом). Каждый вариант — JPEG качества 82, без EXIF. Исходник
     после этого не храни.
6. **Видео** (ffmpeg): до 60 секунд (длиннее — обрезать), 720p, водяной знак `drawtext` в углу; результат —
   **HLS с шифрованием AES-128**: сегменты по 4 с, ключ — 16 случайных байт на ролик, лежит рядом **вне** списка
   отдаваемых файлов. В `.m3u8` адрес ключа — `index.php?a=key&i=<item>` (без токена: токен страница допишет сама,
   см. часть D). Постер — первый кадр, обработанный как картинка из п. 5.
7. Сводка: сколько просмотрено, импортировано, отсеяно (18+, жалобы, дубли, нет файла).

**Проверка:** запусти импорт; в сводке ненулевые цифры; в `/var/lib/amuzer/media` есть варианты картинок и HLS;
открой один вариант (`Read` по пути файла) — он перемешан и с водяным знаком; `ffprobe` по `.m3u8` без ключа не
читает видео. Повторный импорт ничего не дублирует. Число строк в базе Posta и `PRAGMA user_version` Posta не
изменились (сравни до и после).

## Часть C. Сессия, проверка «я браузер» и лимиты

`index.php` — единственная точка входа. Всё, кроме HTML страницы, — JSON или медиа по `?a=…`.
- **Сессия:** кука `amz` (`HttpOnly`, `SameSite=Strict`, `Secure` при HTTPS, 30 дней), id — 32 hex. Без куки любой
  `?a=` → 403.
- **Доказательство работы (proof-of-work):** страница получает `?a=hello` → `{challenge, bits: 18}`; `app.js` через
  WebCrypto ищет `nonce`, при котором `sha256(challenge + nonce)` начинается с 18 нулевых бит, и шлёт `?a=hello_ok`.
  Пока сессия не прошла — лента и медиа отдают 403. Это ~0,3–1 с у человека и дорого для массового скрейпа.
- **Проверка запросов** для `feed`, `m`, `key`, `seg`: `Sec-Fetch-Site: same-origin` (отсутствует или другое → 403);
  `Origin`/`Referer`, если есть, — свой хост.
- **Лимиты** (таблица `hits`): лента — не чаще 1 страницы в 1,5 с и не больше 400 постов за сессию в час; медиа —
  не больше 150 запросов в минуту на сессию и 600 на IP (IP храни только как `hash_hmac` с секретом). Превышение →
  429 и `strikes + 1`; 3 нарушения → бан сессии на час (`banned_until`), страница показывает «Слишком быстро.
  Отдохните немного».
- **Ловушка:** в разметке ленты — невидимая для людей ссылка (`aria-hidden`, вне экрана) на `?a=trap`. Запрос к ней
  → бан сессии на сутки. Добавь `Disallow: /*?a=` в `robots.txt`, чтобы честные поисковики туда не ходили.

**Проверка** (curl): без куки → 403; с кукой без PoW → 403; PoW решить в PHP-скрипте проверки → лента отдаётся;
11 быстрых запросов ленты подряд → 429; `?a=trap` → бан. Удали тестовые сессии.

## Часть D. Лента и выдача медиа

- `?a=feed&cursor=…` → 10 постов: `{id, kind, text (до 500 символов), topic, source_name, w, h, media: [...]}`.
  Порядок — случайный, но **стабильный для сессии** (перемешивание с сидом из id сессии), без повторов до конца
  каталога; потом — по кругу заново перемешанным. `cursor` — подписанный HMAC, а не номер.
- Для каждой картинки в ответе: `src` = `index.php?a=m&t=<токен>` и `map` — перестановка плиток выбранного варианта,
  **закодированная** (XOR с байтами из HMAC от токена и упакованная в base64): без `app.js` из неё не понять порядок.
- **Токен медиа:** `base64url(item|pos|variant|expires|session) + HMAC`. Живёт 120 с, привязан к сессии, работает
  не больше 3 раз (счётчик в `hits`). `?a=m` отдаёт файл варианта (`readfile`), заголовки:
  `Content-Type: image/jpeg`, `Cache-Control: private, no-store`, `Cross-Origin-Resource-Policy: same-origin`,
  `Content-Disposition: inline; filename="amuzer.jpg"`.
- **Видео:** `media: [{type: 'hls', playlist: 'index.php?a=pl&t=<токен>'}]`. `?a=pl` отдаёт `.m3u8`, где ссылки на
  сегменты и ключ заменены на `index.php?a=seg&t=…` / `index.php?a=key&t=…` с токенами этой сессии (сегменты —
  тоже `same-origin`, `no-store`). Ключ отдаётся не больше 3 раз на токен.
- Подпись **«powered by Postå™»** (ссылка на `POSTA_URL`, `target=_blank`, `rel="noopener"`) и название источника
  простым текстом («Источник: Юморист»).

**Проверка:** curl с решённым PoW: лента, токен работает, через 121 с — 403, 4-й раз — 403, с чужой кукой — 403;
скачанный по токену файл — перемешанная картинка.

## Часть E. Страница

`index.php` без `a` отдаёт HTML; всё оформление — в `app.css`, логика — в `app.js`.
- Вертикальная лента на всю высоту, одна колонка, max-width 560px, на телефоне — во всю ширину с отступом 16px.
  Шапка: «amuzer» и маленькое «powered by Postå™».
- **Картинка рисуется только на `<canvas>`:** `app.js` скачивает перемешанный JPEG через `fetch` (не `<img src>`), делает
  `createImageBitmap`, раскодирует `map` и рисует плитки на свои места. В DOM нет `<img>` с адресом контента.
  После отрисовки `ImageBitmap` закрывается, `Blob` освобождается.
- Видео: `hls.js` **локально** в `vendor/` (скачай один раз конкретную версию, например
  `https://cdnjs.cloudflare.com/ajax/libs/hls.js/1.5.13/hls.min.js`; если такой нет — ближайшую 1.5.x, проверь, что
  файл скачался и это JS; запиши версию в отчёт). В Safari — встроенный HLS. Видео без звука, автозапуск при
  попадании в экран, пауза при уходе; клик — звук. `disablePictureInPicture`, `controlsList="nodownload noplaybackrate"`,
  своё управление (пауза, звук) вместо стандартного.
- Бесконечная прокрутка: `IntersectionObserver` подгружает следующую страницу за 3 поста до конца. В DOM живёт
  **не больше 25 карточек**: ушедшие далеко вверх заменяются пустой заглушкой той же высоты (память, плюс нельзя
  разом сохранить страницу). Пока подгружается — скелетон.
- **Защита на клиенте** (это «неудобно», а не «невозможно» — так и напиши в отчёте):
  `contextmenu` и `dragstart` на ленте отменены; `user-select: none` на карточках; `-webkit-touch-callout: none`;
  на canvas — прозрачный слой поверх; при `visibilitychange` в hidden и при `beforeprint` контент закрывается
  размытием; `@media print { .feed { display: none } }`.
- Заголовки страницы: `Content-Security-Policy: default-src 'self'; img-src 'self' blob: data:; media-src 'self' blob:;
  connect-src 'self'; worker-src 'self' blob:; style-src 'self'; script-src 'self'; frame-ancestors 'none'`,
  `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin`, `Permissions-Policy: interest-cohort=()`.
- Пустой каталог / бан / ошибка сети — понятные сообщения словами.
- Подвал: «Правообладателям: напишите на <email из конфига `ABUSE_EMAIL`>, уберём в течение суток» — контент
  чужой, это обязательно.
- Тёмная тема по умолчанию (развлекательная лента), шрифт — системный стек.

**Проверка в браузере** (только `http://localhost/amuzer/` — если сайт ещё не отдаётся Apache, подними
`php -S 127.0.0.1:8098 -t /var/www/html/amuzer` и проверяй там): headless Chrome
`google-chrome --headless --screenshot --window-size=420,900` — лента показывает **собранные** картинки (не мешанину)
и подпись; второй скриншот на 1280×900. Посмотри оба скриншота (`Read`). В консоли нет ошибок CSP (запусти с
`--enable-logging=stderr --v=0` и проверь вывод). Прокрути (через `--run-all-compositor-stages-before-draw` и
`--virtual-time-budget` или маленьким Puppeteer-скриптом, если Node есть) — подгружается вторая страница.

## Часть F. Проверка «на прочность» — попробуй украсть сам

Напиши `app/tools/steal_test.sh` и прогони. Каждая попытка должна **провалиться или дать мусор** — запиши результат:
1. `wget -r http://localhost/amuzer/` — нет ни одного файла контента;
2. curl ленты без PoW, со старым токеном, с токеном из другой сессии;
3. скачать картинку по живому токену → получается перемешанная (посмотри `Read`);
4. скачать `.m3u8` и `ffmpeg -i` по нему с другой сессией → не играется;
5. хотлинк: страница на `php -S 127.0.0.1:8097` с `<img src="http://localhost/amuzer/index.php?a=m&t=…">` — не
   показывается (`Sec-Fetch-Site: cross-site` / CORP);
6. 500 быстрых запросов скриптом → бан.
Чего **не** закрыть (скриншот, запись экрана, выгрузка canvas через DevTools, перехват в DevTools) — честно перечисли
в отчёте.

## Часть G. Обновление каталога

- `app/tools/import.php` идемпотентен и быстр при повторном запуске (пропускает известные `posta_key` до работы с
  файлами).
- `app/tools/cleanup.php`: удаляет просроченные `hits`, сессии старше 30 дней, файлы без записи в `items`.
- Владельцу: «убрать пост» — `php app/tools/remove.php <id>` (удаляет запись и файлы, кладёт `posta_key` в стоп-лист,
  чтобы импорт не вернул) — для жалоб правообладателей.

## Часть H. Развёртывание (подготовить, не выполнять)

Положи в `/var/www/html/amuzer/deploy/`:
- `apache-local.conf`: `<Directory /var/www/html/amuzer> AllowOverride All </Directory>` + запрет доступа к `app/`,
  `deploy/`, `.git` (на случай, если `.htaccess` не читается);
- `amuzer.com.conf`: VirtualHost для домена (`ServerName amuzer.com`, `ServerAlias www.amuzer.com`, `DocumentRoot
  /var/www/html/amuzer`, те же запреты), с комментарием про certbot для HTTPS;
- `cron.txt`: `0 * * * * /usr/bin/php /var/www/html/amuzer/app/tools/import.php >/dev/null 2>&1` и раз в сутки cleanup.
- `.htaccess` в корне сайта: запрет всего, кроме `index.php`, `app.js`, `app.css`, `vendor/*.js`, `robots.txt`,
  шрифтов.
- README коротко: что это, как обновляется, как убрать пост, что защищено и что нет.

## Итоговая проверка

- `php -l` по всем PHP, `node --check app.js`, `bash -n app/tools/steal_test.sh`.
- `curl -s -o /dev/null -w '%{http_code}' http://localhost/amuzer/app/config.php` → 404 (или пустой ответ), то же для
  `app/tools/import.php`, `deploy/cron.txt`; в веб-корне нет ни базы, ни медиа.
- В Posta ничего не изменилось: `git -C /var/www/html/posta status --short` — тот же список, что до начала
  (запиши его в части 0), число строк `users` Posta не изменилось.

## Что сдать

Короткий отчёт владельцу по-русски:
- новые файлы (одна строка на файл);
- сколько постов импортировано и сколько отсеяно (по причинам);
- 2 скриншота (пути к файлам);
- результаты `steal_test.sh` по пунктам и честный список того, что защитить нельзя;
- что сделать владельцу: команды из `deploy/` (Apache, cron, домен, HTTPS), поменять `POSTA_URL` и `ABUSE_EMAIL`;
- что не сделано и почему.
