# Задача: TikTok, YouTube Shorts и X (Twitter) как источники

Ты работаешь в PHP-проекте `/var/www/html/posta` (Posta: ИИ сам подбирает контент по теме и публикует в
Telegram-канал пользователя). Источники сейчас: Telegram, VK, RSS (в том числе YouTube, Pinterest, Tumblr по
`SMMBOX_SOURCES_PROMPT.md`), Двач, Одноклассники, свой контент. Нужно добавить **TikTok**, **YouTube Shorts как
видео** и **X (Twitter)**.

**Сначала проверь**, что `SMMBOX_SOURCES_PROMPT.md` выполнен хотя бы до части B: в `lib/rss.php` есть
`rss_known_feed()` и `rss_feed_kind()`, в `lib/sources.php` есть `PLATFORM_OK`. Если нет — **остановись** и
напиши владельцу, что сначала нужен тот промпт. Эта задача опирается на его код.

## Что проверено на этом сервере 2026-10-23 (перепроверять не нужно)

Общий инструмент для всех трёх — **yt-dlp** (zipapp, один файл, запускается `python3`). Его нет в системе, `pip` нет.
`python3` 3.14, `ffmpeg` и `node` 22 (`/usr/bin/node`) есть.

- **TikTok:** `yt-dlp --flat-playlist --playlist-end 15 -j https://www.tiktok.com/@tiktok` — 15 видео за ~3,4 с.
  В каждой строке JSON: `id`, `url`, `title`, `description`, `timestamp`, `duration`, `view_count`, `like_count`,
  `comment_count`, `repost_count`, `thumbnails` (обложка, подписанный адрес — **протухает**, кэшируй сразу).
  Предупреждение «no impersonate target» — не мешает. Скачивание видео работает (~17 МБ за ролик 105 с), **но
  кодек HEVC** (`hevc 1080x1920`) — его не все клиенты Telegram играют: выбирай H.264-формат, иначе перекодируй.
  Сам HTML профиля (`__UNIVERSAL_DATA_FOR_REHYDRATION__`) отдаёт `"itemList":[]` — без yt-dlp список не получить.
- **YouTube Shorts:** список с датами — лента `https://www.youtube.com/feeds/videos.xml?playlist_id=UUSH<id канала
  без "UC">` (200, работает; это только шортсы канала). `yt-dlp --flat-playlist …/@канал/shorts` даёт ~2,4 с, есть
  `view_count`, но **нет дат**. Скачивание **без JS-рантайма не работает** («Requested format is not available»);
  с `--js-runtimes node` скачалось H.264 720×1280, 3,5 МБ:
  `-f "bv*[vcodec^=avc1][height<=1280]+ba[ext=m4a]/b[ext=mp4]" --merge-output-format mp4`.
- **X (Twitter):** лента пользователя **без входа недоступна**: `yt-dlp https://x.com/NASA` → «Unsupported URL»,
  `syndication.twitter.com/srv/timeline-profile/…` → 429, nitter → 410. **Отдельный твит работает**:
  `yt-dlp -j https://x.com/<user>/status/<id>` (видео) и
  `https://cdn.syndication.twimg.com/tweet-result?id=<id>&token=x` (JSON: текст, `favorite_count`, медиа).

Отсюда план:
```
TikTok-профиль ─┐                        ┌─ обложка в кэш медиа сразу (адрес протухает)
Shorts-канал ───┼─► каталог источников ──┤
                │   (общий, по темам)    └─ видео качается yt-dlp, только когда пост
X: ссылка на ───┘                            попал в подборку (не для каждого кандидата)
твит → «Свой контент» (разово)
X: лента аккаунта → только если владелец дал cookies (часть E), иначе не делать
```

Делай части по порядку: 0 → A → B → C → D → E. Не начинай следующую, пока не прошла проверка предыдущей.

## Общие правила

- **Не делай git commit.** Пользователь проверит изменения сам.
- **НЕ открывай сайт Posta** (`http://localhost/...`) в curl, headless Chrome или Playwright: каждая загрузка без
  куки создаёт нового пользователя и запускает парсер, который нельзя остановить без sudo. Проверяй через `php` и
  `python3` в командной строке, интерфейс — опиши в отчёте, что пользователю проверить глазами.
- **Не трогай `php.ini`, `crontab`, systemd, Apache, sudo.** Не ставь пакеты в систему (`apt`, `pip install` в
  систему, `npm -g`).
- **Ollama не вызывай.** Оценку постов по теме сделает существующий конвейер парсера по обложке и подписи.
- В интерфейсе **не упоминай** конкурентов. Названия площадок пиши так: «TikTok», «YouTube Shorts», «X (Twitter)».
- Код в стиле проекта: короткие функции, комментарии по-русски одной строкой, ошибки — `RuntimeException` с
  понятным русским текстом, SQL — через `db_*`. Обработчики тонкие, логика в `lib/`.
- **Всё внешнее — чужие данные.** На странице только `textContent` / `escapeHtml`. Ссылки — только `https://`.
- **Запуск yt-dlp — только через одну функцию** (часть A), только `proc_open` с **массивом** аргументов (без
  оболочки, без склейки строк), адрес перед передачей проверяется регулярным выражением своей площадки и
  передаётся после `--`. Таймаут на каждый вызов. Никаких `shell_exec`/`exec` со строкой.
- Тесты — CLI-скрипт `tools/test_video_sources.php` по образцу `tools/test_own_content.php` (тестовый
  пользователь, заглушки `json_response`, уборка за собой, `exit($failures ? 1 : 0)`). Сетевые проверки помечай
  `[сеть]`.

## Часть 0. Прочитай целиком

- `SMMBOX_SOURCES_PROMPT.md` — что сделано в прошлой волне и её правила.
- `lib/rss.php` (`rss_known_feed`, `rss_feed_kind`, разбор YouTube и `media:group`), `lib/source_add.php`,
  `lib/ok.php` (как добавлялась последняя площадка — повторяй тот же путь), `lib/own_content.php`.
- `lib/sources.php`: константы площадок, `CATALOG_KEYS`, `SOURCE_NAME_PREFIXES`, `source_id_for`,
  `is_valid_source_id`, `is_trusted_source_url`, `build_source_preview_posts`, `load_source_db`, `update_source_db`.
- `lib/parser.php`: `collect_catalog_source`, `collect_posts_by_platform`, `post_item_video_promote`,
  `add_item_to_pool_safely`, `fetch_candidate_poster`, `run_collector`; что такое `video_unavailable`.
- `lib/content.php`: `media_cache_store_video`, `cache_downloaded_video`, `media_cache_path`, `media_cache_cleanup`,
  `pool_without_lost_video`, `post_item_has_video`. Найди **все** места, где видео поста скачивается в кэш
  (`grep -rn "media_cache_store_video\|cache_downloaded_video" lib handlers tools`) — туда встроится yt-dlp.
- `lib/telegram.php` — как публикация берёт видео из кэша и какой у неё предел размера файла.
- `lib/core.php` — `fetch_public`, `fetch_public_image`, `validate_image_bytes`, константы путей данных
  (где лежат `data/`, кэш медиа, временные файлы).
- `tools/tg_user.py` и как PHP его вызывает — образец запуска Python из PHP в этом проекте.
- `handlers/sources.php`, `js/sources.js`, вкладка «Источники» в `index.html`, `js/core.js` (`safeSourcePostUrl`).

## Часть A. yt-dlp в проекте и одна функция запуска

1. Скачай последний релиз zipapp в `tools/bin/yt-dlp`:
   `curl -sL -o tools/bin/yt-dlp https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp` и сверь
   SHA-256 с файлом `SHA2-256SUMS` того же релиза (строка для `yt-dlp`). Не совпало — удали файл и остановись.
   Права `0755`. Версию запиши в `tools/bin/YT_DLP_VERSION`. Скрипт обновления `tools/update-yt-dlp.sh` (то же
   скачивание + проверка суммы + замена атомарно через временный файл) — yt-dlp ломается, когда площадки меняются,
   его придётся обновлять. В `.gitignore` **не** добавляй: владелец решит сам.
2. `lib/ytdlp.php` (в `bootstrap.php`):
   - `ytdlp_run(array $args, int $timeout): array` → `['ok', 'code', 'stdout', 'stderr']`. Команда:
     `python3 -I tools/bin/yt-dlp --ignore-config --no-warnings --no-progress --js-runtimes node:/usr/bin/node
     --cache-dir <каталог данных проекта>/ytdlp-cache --socket-timeout 15 ...$args`. `proc_open` с массивом,
     `stdin` закрыт, чтение `stdout`/`stderr` неблокирующее, по таймауту — `proc_terminate` и `ok=false`. Каталог
     кэша создаётся, если нет, с правами как у остального каталога данных (парсер и сайт работают от разных
     пользователей — посмотри, как это решено для кэша медиа, и сделай так же).
   - `ytdlp_list(string $url, int $count): array` — `--flat-playlist --playlist-end $count -j -- $url`, разбор
     построчного JSON, таймаут 30 с.
   - `ytdlp_download(string $url, string $format, string $dest_dir, int $max_bytes): string` — путь к файлу mp4
     или `RuntimeException`. Аргументы: `-f $format --merge-output-format mp4 --max-filesize <max> --no-playlist
     -o <dest_dir>/%(id)s.%(ext)s -- $url`, таймаут 180 с. Предел размера — тот же, что публикация в Telegram
     принимает для видео (найди константу), не больше.
   - `ytdlp_h264(string $file): string` — если видеопоток не `h264` (`ffprobe -v error -select_streams v:0
     -show_entries stream=codec_name -of csv=p=0`), перекодируй `ffmpeg -c:v libx264 -preset veryfast -crf 23
     -c:a aac -movflags +faststart` во временный файл и замени. `ffprobe`/`ffmpeg` — тоже `proc_open` с массивом.
     Если в проекте уже есть функция перекодирования (поищи `ffmpeg` в `lib/`) — используй её.
   - Одновременно — **не больше одного** скачивания на сервер (`try_lock` на файл блокировки, как у публикатора):
     сервер слабый, а yt-dlp + ffmpeg тяжёлые.

**Проверка:**
```bash
python3 -I tools/bin/yt-dlp --version
php -l lib/ytdlp.php
php -r 'require "bootstrap.php"; $r = ytdlp_list("https://www.tiktok.com/@tiktok", 3); echo count($r), " ", $r[0]["id"] ?? "-", PHP_EOL;
$r = ytdlp_run(["--version"], 1); var_dump($r["ok"]);'   # второй — таймаут 1 с: не должен виснуть
```
В тест: попытка передать адрес `--exec=touch /tmp/x` или `https://x.com/a; id` не запускает ничего лишнего
(функции площадок отвергают такой адрес до вызова; а если передать в `ytdlp_list` напрямую — после `--` это
просто неверный адрес).

## Часть B. TikTok — площадка каталога

1. `lib/sources.php`: `PLATFORM_TIKTOK = 'TikTok'`, префикс `'TikTok | '`, `CATALOG_KEYS` → `'tiktok_accounts'`,
   `source_id_for` → `tiktok:`, `is_valid_tiktok_user($u)` — `^[A-Za-z0-9_.]{2,24}$`. Добавь `tiktok_accounts`
   во **все** места, где перечислены виды каталога (`grep -rn "ok_groups" lib handlers tools js` — везде, где есть
   `ok_groups`, должен быть и TikTok). Запись каталога не теряет строки (счёт `catalog_sources` по `kind` до и после
   `update_source_db(fn($db) => $db)`).
2. `lib/tiktok.php`:
   - `tiktok_user_from_link(string $input): string` — `@name`, `tiktok.com/@name`, `www.tiktok.com/@name/…`,
     `m.tiktok.com/@name`. Ссылка на одно видео (`/@name/video/<id>`) — тоже даёт `name` (добавляем автора).
     Короткие `vm.tiktok.com/…` / `vt.tiktok.com/…` — разверни редирект через `fetch_public` без тела
     (только заголовок `Location`), затем разбери.
   - `tiktok_fetch_posts(string $user, int $count): array` — через `ytdlp_list("https://www.tiktok.com/@$user", $count)`.
     Пост: `image` — обложка (самая большая из `thumbnails`), `video` — **пустой**, `video_page` —
     `https://www.tiktok.com/@$user/video/<id>`, `video_via` — `'ytdlp'`, `text` — `description` (или `title`),
     `posted_at` — `timestamp`, `url` — `video_page`, `views`, `likes`, `comments`, `reposts`, `duration`.
     Видео длиннее 3 минут пропускай. Посты без обложки пропускай.
   - **Обложка протухает**: в `collect_catalog_source` для TikTok сразу кэшируй обложку
     (`fetch_public_image` → `media_cache_store_image`) и подставляй адрес кэша в `image`/`images`; не скачалась —
     пост пропусти. Проверь, что `media_cache_cleanup` не удалит обложку кандидата, пока он в очереди анализа;
     если удалит — разберись, как это решено для других площадок, и сделай так же.
3. Подключи площадку везде, где подключены Одноклассники: `source_parse_link` (перед общей веткой `https?://`),
   `source_probe`, `collect_catalog_source` (ярлык `@user`, подписчиков нет — `0`, `$rate = null`; не чаще одного
   запроса yt-dlp на аккаунт за проход и не больше `TIKTOK_POSTS_PER_ACCOUNT = 12` постов — константа рядом с
   `VK_POSTS_PER_PUBLIC`), `collect_posts_by_platform`, `build_source_preview_posts`, `handlers/sources.php`
   (описание кандидата «Аккаунт TikTok с короткими видео.»), `is_trusted_source_url` и `safeSourcePostUrl`
   (`www.tiktok.com`), метка в `js/sources.js`, подсказка под полем «Добавить источник».
4. Сбор не должен вешать парсер: если yt-dlp упал или кончился таймаут — запись в журнал, источник пропускается
   в этом проходе (как недоступная страница Telegram), **без** заморозки с первого раза.

**Проверка:**
```bash
php -l lib/tiktok.php lib/sources.php lib/parser.php lib/source_add.php handlers/sources.php
php -r 'require "bootstrap.php";
foreach (["@tiktok", "https://www.tiktok.com/@tiktok/video/7698770783367171359", "https://www.tiktok.com/@a;id", "https://www.tiktok.com/tag/cat"] as $l) {
  try { echo $l, " → ", tiktok_user_from_link($l), PHP_EOL; } catch (Throwable $e) { echo $l, " → ошибка: ", $e->getMessage(), PHP_EOL; } }
foreach (tiktok_fetch_posts("tiktok", 3) as $p) echo date("c", $p["posted_at"]), " | ▶", $p["views"], " | ", mb_substr($p["text"], 0, 50), PHP_EOL;'
php -r 'require "bootstrap.php"; echo json_encode(source_add_manual("https://www.tiktok.com/@tiktok", "тест-тикток"), JSON_UNESCAPED_UNICODE), PHP_EOL;'
```
Посты попадают в `collect_posts_by_platform(load_source_db(), "тест-тикток")` с обложками **в кэше медиа**.
**Удали тестовую запись** из каталога и обложки из кэша.

## Часть C. Видео через yt-dlp, когда пост попал в подборку

Скачивать ролик для каждого кандидата нельзя: кандидатов десятки, а ролик — 5–20 МБ и до минуты работы. Видео
нужно, только когда пост попал в подборку пользователя.

1. Найди место, где видео поста (`video`) оказывается в кэше медиа для Telegram/VK, и добавь ветку: если у записи
   `video_via === 'ytdlp'` и `video` пустой — **не в момент добавления в пул**, а фоновой задачей.
   Сделай очередь: таблица или `kv` `ytdlp_queue` (`user_id`, `image_key` записи, `video_page`, `tries`,
   `next_at`). Обработчик очереди — `tools/ytdlp_worker.php` (CLI, `try_lock`, не дольше 50 с за запуск, по одной
   задаче), запуск — так же, как фоново запускается публикатор со страницы (найди, как это сделано, и повтори;
   cron не трогай). Скачал → `ytdlp_h264` → `media_cache_store_video` / `cache_downloaded_video` → в записи пула
   выставь `video` (через `pool_update` по `content_image_key`). Не вышло 3 раза — `video_unavailable = true`
   (дальше работает существующая логика).
2. Пока видео нет, карточка в подборке показывает обложку и пометку «Видео загружается…» (найди, как карточка
   показывает видео, и добавь состояние). Публикация такого поста: если видео ещё нет — ошибка «Видео ещё
   загружается, попробуйте через минуту», а не публикация картинки вместо видео.
3. Формат для TikTok: `b[vcodec^=h264]/b[vcodec^=avc1]/b[ext=mp4]/b` (потом `ytdlp_h264` на всякий случай).
   Для Shorts — из части D.

**Проверка:** в `tools/test_video_sources.php` — тестовый пользователь, запись TikTok-поста в его пул (из
`tiktok_fetch_posts`, через `add_item_to_pool_safely`), постановка в очередь, один запуск
`php tools/ytdlp_worker.php` → у записи `video` указывает на файл в кэше, `ffprobe` даёт `h264`, размер меньше
предела публикации. Повторный запуск — ничего не делает. Удали файлы и пользователя.

## Часть D. YouTube Shorts — видео, а не обложка

В прошлой волне YouTube — это RSS: обложка + ссылка на ролик в тексте. Для шортсов этого мало: нужен сам ролик.

1. `rss_known_feed()`: ссылка `https://www.youtube.com/@канал/shorts` (и `/channel/UC…/shorts`) → лента
   **только шортсов** `https://www.youtube.com/feeds/videos.xml?playlist_id=UUSH<channel_id без "UC">`.
   Обычная ссылка на канал остаётся как была (все ролики). `rss_feed_kind()` для `playlist_id=UUSH…` →
   `YouTube Shorts`.
2. В разборе ленты: для записей ленты `UUSH…`, а также для любых записей YouTube, где ссылка `/shorts/<id>`:
   `video` пустой, `video_via = 'ytdlp'`, `video_page = https://www.youtube.com/shorts/<id>`, и **не** добавляй
   ссылку на ролик в конец текста (видео будет в самом посте). Обычные длинные ролики — как раньше.
3. Формат: `bv*[vcodec^=avc1][height<=1280]+ba[ext=m4a]/b[ext=mp4]`. Без `--js-runtimes node` YouTube не отдаёт
   форматы — `ytdlp_run` уже передаёт его; проверь, что парсер и сайт (разные пользователи ОС) оба видят
   `/usr/bin/node`.
4. Подсказка под полем «Добавить источник»: «YouTube Shorts: ссылка вида youtube.com/@канал/shorts».

**Проверка:**
```bash
php -r 'require "bootstrap.php"; $s = source_parse_link("https://www.youtube.com/@MrBeast/shorts"); echo $s["id"], " ", rss_feed_kind($s["id"]), PHP_EOL;
foreach (rss_fetch_posts($s["id"], 3) as $p) echo $p["video_via"] ?? "-", " ", $p["video_page"] ?? "-", " ", $p["image"], PHP_EOL;'
```
Затем то же, что в части C, но для шорта: запись в пул тестового пользователя → воркер → `h264`, вертикальное видео.

## Часть E. X (Twitter)

**Ленту аккаунта без входа читать нельзя** (проверено выше). Поэтому:

1. **Твит по ссылке — в «Свой контент».** В блоке «Свой контент» поле «Ссылки на посты X (Twitter)» (до 20 ссылок,
   по одной на строку). `lib/x_posts.php`:
   - `x_status_id(string $url): ?string` — `https://(x|twitter|mobile.twitter).com/<user>/status/<id>` (id — цифры
     до 20 знаков), остальное → `null`;
   - `x_fetch_post(string $id): array` — `fetch_public("https://cdn.syndication.twimg.com/tweet-result?id=$id&token=x")`
     (токен в этом адресе — заглушка, проверено: работает): текст, дата, `favorite_count`, картинки
     (`mediaDetails[].media_url_https`, только `https://pbs.twimg.com/`), видео (`video_info.variants` — mp4 с
     наибольшим `bitrate`, только `https://video.twimg.com/`). Если syndication не отдал медиа, а пост с видео —
     `video_via = 'ytdlp'`, `video_page` = ссылка на твит (yt-dlp умеет отдельные твиты).
     Твит без медиа — ошибка строки «В посте нет картинки или видео»;
   - `x_import_links(array $links): array` → `['added', 'errors' => [[ссылка, текст]]]`: картинки — через
     `own_store_image`, посты — `own_post_item` + `own_add_to_pool` (как CSV), видео — сразу скачать
     (`fetch_public` по `video.twimg.com`) или через очередь части C.
   - Обработчик `handle_own_import_x` (POST), в список `POST`. Лимит — 100 ссылок в сутки на пользователя.
   - `is_trusted_source_url` / `safeSourcePostUrl` — `x.com`, ссылка «Открыть исходный пост» ведёт на твит.
2. **Лента аккаунта X — только если владелец сам дал доступ.** Это возможно только с cookies залогиненного
   аккаунта X (и инструментом вроде gallery-dl); X банит такие аккаунты, поэтому решение за владельцем.
   Проверь, есть ли файл `data/x_cookies.txt`. **Если файла нет — эту часть не делай**, а в отчёте опиши одним
   абзацем, что нужно владельцу (запасной аккаунт X, экспорт cookies в формате Netscape в `data/x_cookies.txt`,
   риск бана аккаунта) и что тогда будет сделано. Если файл есть — остановись и спроси владельца, прежде чем
   что-то делать с ним.

**Проверка:** в тест — разбор ссылок (`x.com`, `twitter.com`, `mobile.twitter.com`, ссылка на профиль → `null`,
`/status/abc` → `null`); `[сеть]` `x_fetch_post("1585341984679469056")` — есть текст и медиа; импорт 1–2 живых
твитов с картинкой тестовому пользователю → записи в пуле с картинками в кэше.

## Отчёт

1. По частям: что сделано, какие файлы, вывод `php tools/test_video_sources.php` целиком.
2. Что пользователю проверить глазами в браузере — по пунктам (добавить TikTok-аккаунт, шортсы канала, импорт
   твита; видео в подборке появляется после «Видео загружается…»; публикация видео в канал).
3. Сколько времени в среднем занимает: список TikTok-аккаунта, скачивание ролика TikTok, шорта (с перекодированием
   и без).
4. Что хрупкое: yt-dlp ломается при изменениях TikTok/YouTube — команда обновления `tools/update-yt-dlp.sh`;
   syndication X может закрыться в любой момент.
5. Про ленту X — абзац из части E.2.
