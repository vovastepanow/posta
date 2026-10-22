# Задача: источники как у SMMbox — своя ссылка, RSS, CSV, файлы, API

Ты работаешь в PHP-проекте `/var/www/html/posta` (Postä: ИИ сам подбирает контент по теме из Telegram, VK и
Двача и публикует в канал пользователя). Главный конкурент — SMMbox: он ничего не ищет сам, но берёт контент из
Telegram, VK, Instagram, RSS, CSV-файлов, папок с файлами и через API. Нужно догнать его по источникам, сохранив
наш автоподбор.

Что есть сейчас (проверено, перепроверять не нужно):
- площадки: Telegram (`t.me/s/<канал>`), VK (через OAuth), Двач. Константы в `lib/sources.php:4-9`;
- каталог источников **общий для всех пользователей** (`catalog_sources`, `load_source_db()` / `update_source_db()`),
  у записи есть `topics` — к каким темам она относится;
- источники в каталог попадают **только** через автоподбор (`lib/discovery.php`) и голосование «Источники»
  (`handlers/sources.php`). **Добавить свой источник по ссылке нельзя** — это первое, что надо сделать;
- своего контента (загрузить файлы или таблицу) нет. Кнопка «Добавить» в подборе (`openNewItemEditor`) — это
  редактор одного поста для разовой публикации, не библиотека.

Что нужно получить:
```
Вкладка «Источники» → «Добавить источник»: ссылка на Telegram-канал, паблик VK или сайт/RSS
        → проверка (есть ли посты с картинками) → в каталог с темой пользователя → парсер собирает как обычно
Вкладка «Источники» → «Свой контент»: загрузить картинки или CSV → посты сразу в подборку пользователя
API: POST с токеном пользователя → пост в подборку (для Make, n8n, своих скриптов)
```

**Instagram не делаем.** Публичного способа читать чужие аккаунты без входа нет, скрейпинг нарушает правила
Meta, а Meta в РФ признана экстремистской организацией: упоминание Instagram в продукте и рекламе требует
оговорок. «Папки» SMMbox закрываем загрузкой файлов (часть D). В отчёте одной строкой напиши, что Instagram
пропущен и почему.

Делай части по порядку: 0 → A → B → C → D → E → F. Не начинай следующую, пока не прошла проверка предыдущей.
Часть F (API) — **только если A–E прошли полностью**; иначе остановись и напиши, на чём застрял.

## Общие правила

- **Не делай git commit.** Пользователь проверит изменения сам.
- **НЕ открывай сайт** (`http://localhost/...`) в curl, headless Chrome или Playwright. Каждая загрузка страницы без
  куки создаёт нового пользователя и запускает парсер, который нельзя остановить без sudo. Сервер проверяй через
  `php` в командной строке, интерфейс — глазами пользователя (опиши в отчёте, что ему проверить).
- **Не трогай `php.ini`, `crontab`, systemd, Apache и sudo.** Лимиты загрузки на сервере: `upload_max_filesize = 2M`,
  `post_max_size = 8M`. Их не меняй — показывай пользователю понятную ошибку.
- **Модель Ollama не вызывай** ни в коде новых частей, ни в проверках: видеокарта на 3 ГБ, модель нужна парсеру
  пользователя. Оценку постов по теме сделает существующий конвейер парсера.
- Код в стиле проекта: короткие функции, комментарии по-русски одной строкой, ошибки — `RuntimeException` с понятным
  русским текстом, SQL — через `db()`, `db_one`, `db_all`, `db_exec`, `db_tx`. Новые обработчики — тонкие, логика в `lib/`.
- **Всё, что пришло извне (RSS, CSV, файлы, API), — чужие данные.** На странице только через `textContent` /
  `escapeHtml`. Ссылки — только `https://`. Сетевые запросы к адресам от пользователя — **только** через
  `fetch_public()` из `lib/core.php` (она не пускает во внутреннюю сеть). `http_get()` для них не используй.
- Новые POST-действия добавляй в список `POST` в `index.php`, GET — в список `GET`.

## Часть 0. Прочитай целиком

- `lib/sources.php` — особенно строки 1–30 (константы, `source_id_for`, `is_trusted_source_url`), `fetch_telegram_channel_page`,
  `telegram_channel_info`, `telegram_posts_from_html`, `build_vk_preview_posts`, `vk_search_groups`,
  `build_source_preview_posts`, `source_preview_images`, `load_source_db`, `update_source_db`, `catalog_entry_matches_topic`.
- `lib/parser.php` — `collect_catalog_source`, `collect_posts_by_platform` (как пост выглядит после сбора:
  `image`, `images`, `video`, `captions`, `text`, `posted_at`, `source`, `source_url`, `source_key`, `gallery`).
- `lib/content.php` — `content_image_key`, `source_post_url`, `media_cache_write`, `media_cache_name`,
  `media_cache_store_image`, `media_cache_path`, `media_cache_cleanup` (файлы кэша, на которые не ссылается пул, удаляются).
- `lib/core.php` — `fetch_public`, `public_url_resolve`, `validate_image_bytes`.
- `lib/db_access.php` — `pool_load`, `pool_update`, `kv_get`, `kv_update`, `catalog_load`, `catalog_update`.
- `lib/db.php` — как устроены миграции; найди **последний** номер миграции (новая будет следующей).
- `handlers/sources.php` целиком, `js/sources.js` целиком, разметку вкладки «Источники» в `index.html`.
- `js/core.js` — `safeSourcePostUrl` (список разрешённых хостов на фронте), `api()`, `el()`.
- `index.php` — списки `GET`/`POST`, проверка `X-Posta` для POST, `ROUTE_HANDLERS`.
- `lib/telegram.php:230-250` — как публикация берёт файл из кэша (`media_cache_path`): свой контент должен лежать в
  кэше медиа, тогда публикация в Telegram заработает без доработок.

## Часть A. Подготовка: площадка RSS и ловушка в `source_id_for`

1. **Ловушка:** `source_id_for()` сейчас возвращает `'vk:'` для **любой** площадки, кроме Telegram. Перепиши через
   `match`: Telegram → `telegram:`, VK → `vk:`, RSS → `rss:`. Неизвестная площадка — `InvalidArgumentException`.
   Найди все вызовы (`grep -rn "source_id_for" lib handlers tools`) и убедись, что Двач туда не попадает.
   Та же ловушка в `is_valid_source_id()` (строка 21): всё, что не Telegram, проверяется как домен VK, и адрес ленты
   её не пройдёт. Тоже `match`: RSS → `https://`-адрес до 500 символов (`filter_var(..., FILTER_VALIDATE_URL)`).
   Найди её вызовы и проверь, что RSS-записи каталога не отбрасываются как «битые».
2. В `lib/sources.php` добавь `const PLATFORM_RSS = 'RSS';`, префикс `'RSS | '` в `SOURCE_NAME_PREFIXES`,
   `PLATFORM_RSS => 'rss_feeds'` в `CATALOG_KEYS`.
3. Виды каталога (`2ch_boards`, `vk_publics`, `telegram_channels`) перечислены вручную минимум в **трёх** местах
   `lib/sources.php`: нормализация с умолчаниями (около строки 541), `load_source_db()` (около 556) и
   `update_source_db()` (около 577). Добавь `rss_feeds` (по умолчанию `[]`) во все три — иначе записи RSS пропадут
   при первой же записи каталога. Поищи ещё: `grep -n "telegram_channels" lib/*.php handlers/*.php`.
4. Везде, где площадки перебираются через `CATALOG_KEYS` (`grep -rn "CATALOG_KEYS" lib handlers`), проверь, что
   RSS не ломает код: например, VK пропускается без OAuth — для RSS такой проверки не нужно.

**Проверка:**
```bash
php -l lib/sources.php
php -r 'require "bootstrap.php"; echo source_id_for(PLATFORM_RSS, "https://example.com/feed"), PHP_EOL, source_id_for(PLATFORM_TELEGRAM, "Abc"), PHP_EOL;
$db = load_source_db(); echo isset($db["rss_feeds"]) ? "rss_feeds есть\n" : "НЕТ rss_feeds\n";'
```
Затем убедись, что запись каталога не теряет данные: посчитай строки `catalog_sources` по `kind` до и после
`update_source_db(fn($db) => $db);` — числа должны совпасть.

## Часть B. Чтение RSS/Atom

Новый файл `lib/rss.php` (подключи, добавив `'rss'` в конец списка в `bootstrap.php`).

- `rss_fetch_posts(string $feed_url, int $count): array` — посты в том же формате, что у Telegram
  (`telegram_posts_from_html`), самые свежие первыми:
  - загрузка через `fetch_public($feed_url, 2 * 1024 * 1024, null, 15)`; не `ok` → `RuntimeException('Лента не загрузилась')`;
  - разбор: `libxml_use_internal_errors(true)`, `simplexml_load_string($bytes, 'SimpleXMLElement', LIBXML_NONET)`.
    **Не используй `LIBXML_NOENT`** (внешние сущности — дыра XXE);
  - RSS 2.0 (`channel/item`) и Atom (`feed/entry`). Пространства имён `media:` (`http://search.yahoo.com/mrss/`) и
    `content:encoded` читай через `->children($ns)`;
  - картинка поста по порядку: `enclosure` с `type` image/* → `media:content` с `medium="image"` или type image/* →
    `media:thumbnail` → первый `<img src>` в `description` / `content:encoded` / Atom `content`. Только `https://`;
  - видео: `enclosure` / `media:content` с `type` `video/mp4` (только `https://`); у такого поста картинка обязательна
    (постер), иначе пропусти пост;
  - текст: заголовок + описание без HTML (`clean_html_text` из проекта), до 1000 символов;
  - `posted_at` из `pubDate` / `published` / `updated` (`strtotime`, ошибка → 0);
  - `url` — ссылка на запись (`link` или Atom `link[rel=alternate]`), только `https://`, иначе `''`;
  - посты без картинки пропускай (конвейер работает с картинками). Не больше `$count` постов.
- `rss_discover(string $url): string` — пользователь вставит адрес сайта, а не ленты. Загрузи страницу через
  `fetch_public` (до 1 МБ); если это уже RSS/Atom (корень `rss`/`feed`) — верни `$url`; иначе найди
  `<link rel="alternate" type="application/rss+xml|application/atom+xml" href="…">`, сделай ссылку абсолютной
  относительно `$url`, верни её. Не нашлось → `RuntimeException('На этой странице нет RSS-ленты')`.
- `rss_feed_title(string $feed_url): string` — название ленты (`channel/title` или `feed/title`), иначе хост.

Подключи RSS в сбор:
- `collect_catalog_source()` в `lib/parser.php`: сейчас там `if Telegram … else VK`. Добавь ветку RSS:
  `$posts = rss_fetch_posts($entry['id'], RSS_POSTS_PER_FEED)`, `$times = array_column($posts, 'posted_at')`,
  подписчиков нет (0), `$rate = null`. Константу `RSS_POSTS_PER_FEED = 15` добавь в `config.php` рядом с
  `VK_POSTS_PER_PUBLIC`. Ярлык в журнале — хост ленты.
- `collect_posts_by_platform()`: в начальный массив `$by` добавь `PLATFORM_RSS => []`. Проверь, что дальше по
  функции (и там, где `$by` используется после неё) RSS не теряется и не ломает подсчёты.
- `build_source_preview_posts()`: ветка `PLATFORM_RSS => rss_fetch_posts($source['id'], $count)`, исключения
  ловить так же, как остальные ветки (пустой список).

**Ссылка «Открыть исходный пост».** Сейчас её показывают только для `2ch.hk`, `t.me`, `vk.com` — и на сервере
(`is_trusted_source_url`, `TRUSTED_SOURCE_HOSTS`), и на фронте (`safeSourcePostUrl` в `js/core.js`). Для RSS:
- в `source_post_url()` (`lib/content.php`): если `source_key` начинается с `rss:`, а `source_url` — `https://` и его
  хост совпадает с хостом ленты из `source_key` или является его поддоменом (или наоборот: `www.site.ru` ↔
  `site.ru`) — вернуть ссылку. Иначе `''`;
- в `safeSourcePostUrl`: для `record.source_key`, начинающегося с `rss:`, разрешить любой `https:`-адрес (сервер уже
  проверил его в `source_post_url`). Для остальных — как сейчас.

**Проверка** (сеть нужна, модель — нет). Найди 2 живые публичные ленты: например, через `rss_discover` на главной
странице крупного новостного сайта или блога с картинками. Затем:
```bash
php -l lib/rss.php lib/parser.php lib/content.php
php -r 'require "bootstrap.php"; $f = rss_discover("<адрес сайта>"); echo $f, PHP_EOL, rss_feed_title($f), PHP_EOL;
foreach (rss_fetch_posts($f, 5) as $p) echo date("c", $p["posted_at"]), " | ", mb_substr($p["text"], 0, 60), " | ", $p["image"], PHP_EOL;'
```
У постов есть картинки, даты и текст. Проверь и XXE: подай `rss_fetch_posts` на локальный файл нельзя (только
https), поэтому проверь сам разбор — вынеси его в `rss_parse(string $xml, int $count)` и вызови с лентой, где есть
`<!DOCTYPE x [<!ENTITY e SYSTEM "file:///etc/passwd">]>` и `&e;` в заголовке: в тексте **не** должно появиться
содержимое `/etc/passwd`.

## Часть C. «Добавить источник» по ссылке

`lib/sources.php` (или новый `lib/source_add.php`, тогда подключи его в `bootstrap.php`):

`source_parse_link(string $input): array` → `['platform' => …, 'id' => …]` или `RuntimeException`:
- Telegram: `@name`, `t.me/name`, `https://t.me/name`, `https://t.me/s/name` → `PLATFORM_TELEGRAM`, id = `name`
  (проверка `is_valid_telegram_channel`). Ссылки-приглашения (`t.me/+…`, `joinchat`) → ошибка «Закрытые каналы не
  читаются: нужна публичная ссылка t.me/имя»;
- VK: `vk.com/name`, `https://vk.com/name`, `club123`, `public123` → `PLATFORM_VK`, id = `name`
  (проверка `is_valid_vk_domain`);
- всё остальное с `http(s)://` → `PLATFORM_RSS`, id = результат `rss_discover()` (адрес ленты).

`source_add_manual(string $input, string $topic): array`:
1. разобрать ссылку; RSS и VK — проверить, что площадка доступна (для VK — `vk_is_connected()`, иначе ошибка
   «Сначала подключите VK на вкладке «Источники»»);
2. проверить, что есть хотя бы 1 пост с картинкой за `CONTENT_MAX_AGE_DAYS` (Telegram — `telegram_channel_info`,
   VK — `build_vk_preview_posts`, RSS — `rss_fetch_posts`). Нет — ошибка «В источнике нет свежих постов с картинками»;
3. в `update_source_db`: если источник уже есть в каталоге — **добавь тему** в его `topics` (без дублей), иначе
   добавь запись `['id', 'name', 'topics' => [$topic], 'manual' => true, 'preview' => source_preview_images($posts)]`.
   Если у пользователя был голос «не нравится» за этот источник по этой теме (`source_votes`) — удали его;
4. вернуть `['platform', 'id', 'name', 'added' => bool, 'posts' => n]`.

Обработчик `handle_add_topic_source($in)` в `handlers/sources.php`: `$in['link']` (строка до 300 символов), тема —
`current_topic()`. Тема 18+ (`topic_save_denied(current_topic()) !== null`) → ошибка. Ответ `ok([...])`.
Добавь `'add_topic_source'` в `POST` и в маршруты `index.php`.

Фронт (`js/sources.js` + разметка в `index.html` над списком источников темы): поле «Ссылка на канал, паблик или
сайт», кнопка «Добавить», строка статуса. Подсказка под полем: «Telegram: t.me/канал · VK: vk.com/паблик · Сайт или
блог: адрес страницы, найдём RSS сами». После успеха — статус «Добавлен: <name>» (через `textContent`) и перезагрузка
списка источников темы (функция, которая сейчас рисует `topicSourceCard`). В карточке источника RSS платформа
показывается как «RSS» (`source-platform-tag`).

Проверь `handle_topic_sources()`: он собирает карточки по площадкам — RSS-записи должны в нём появиться
(название, `url` ленты, превью). `url` для RSS — адрес ленты.

**Проверка** (в CLI, без браузера; не трогай реальную тему пользователя — используй тестовую тему):
```bash
php -r 'require "bootstrap.php";
foreach (["@durov", "https://t.me/+abc", "vk.com/team", "<адрес сайта из части B>"] as $l) {
  try { echo json_encode(source_parse_link($l), JSON_UNESCAPED_UNICODE), PHP_EOL; } catch (Throwable $e) { echo "ошибка: ", $e->getMessage(), PHP_EOL; } }
echo json_encode(source_add_manual("<адрес сайта>", "тест-источники"), JSON_UNESCAPED_UNICODE), PHP_EOL;'
```
Убедись, что запись появилась в `rss_feeds` с темой `тест-источники`, а `php -r '...collect_posts_by_platform(load_source_db(), "тест-источники")...'`
вернул посты RSS (если функция печатает журнал — это нормально). **Удали тестовую запись** из каталога после проверки.

## Часть D. «Свой контент»: файлы и CSV

Свой контент пользователя — **личный**, в общий каталог он не попадает. Посты кладутся сразу в начало подборки
пользователя (`pool_update`), без оценки моделью: пользователь сам их выбрал.

`lib/own_content.php` (подключи в `bootstrap.php`):

- `own_post_item(array $media, string $text, string $label): array` — запись пула:
  `['image' => <локальный URL картинки>, 'images' => [...], 'image_local' => <тот же URL>, 'video' => '', 'text' => $text,
  'captions' => [$text], 'source' => 'Свой контент · ' . $label, 'source_key' => 'own:' . app_user_id(),
  'source_url' => '', 'posted_at' => time(), 'topic' => current_topic(), 'own' => true]` (+ `gallery`, если файлов
  несколько — формат как в `content_gallery`). Сверь поля с тем, что кладёт в пул парсер, и добавь недостающие
  обязательные (например, `rating`/`relevance`, если карточка без них ломается — посмотри `content_for_display`).
- Картинки сохраняй через `validate_image_bytes()` + `media_cache_store_image(<уникальный ключ>, $bytes)`. Ключ —
  `'own:' . app_user_id() . ':' . sha1($bytes)`: одинаковый файл не дублируется. Файлы кэша удаляет
  `media_cache_cleanup`, если на них не ссылается пул, — запись в пуле их удерживает, отдельного хранилища не нужно.
- Видео в этой части **не** принимай: при лимите 2 МБ на файл видео почти никогда не пролезет. Ошибка:
  «Видео пока не загружается: добавьте его через редактор поста».

Действия:
1. `own_upload` (POST, `multipart/form-data`, поле `files[]` до 10 файлов + `text`): каждый файл → отдельный пост
   (или один пост-альбом, если отмечена галочка «Одним постом»). `$_FILES` с `error = UPLOAD_ERR_INI_SIZE` или
   `UPLOAD_ERR_FORM_SIZE` → «Файл больше 2 МБ: так настроен сервер». Пустой `$_FILES` при большом запросе (PHP
   отбрасывает тело больше `post_max_size`) → «Слишком много за раз: до 8 МБ в одной загрузке».
   Проверь, как `index.php` читает тело POST: сейчас обработчики получают JSON. Для `multipart` возьми `$_POST`/`$_FILES`,
   не ломая остальные действия. Проверка `X-Posta` должна остаться.
2. `own_import_csv` (POST, файл `csv` до 1 МБ): разделитель `;` или `,` (определи по первой строке), UTF-8 (BOM
   срежь; если строка не UTF-8 — попробуй `mb_convert_encoding` из Windows-1251). Колонки по заголовку, регистр не
   важен: `text` / `текст`, `image` / `картинка` / `image_url` (одна или несколько ссылок через пробел или `|`),
   `date` / `дата` (необязательно). Каждая картинка — `fetch_public_image()` (не `http_get`), до 10 строк-ошибок
   собираются в ответ с номером строки. Не больше 100 строк за раз. Ответ: `{added, skipped, errors: [...]}`.
3. Фронт: в «Источниках» блок «Свой контент» — две кнопки: «Загрузить картинки» (`<input type=file multiple
   accept="image/*">`) и «Импорт из CSV» (+ ссылка «Пример CSV», которая скачивает файл-образец, собранный на
   клиенте через `Blob`: заголовок `text;image` и две строки-примера). Отправка — `FormData` через `fetch` с
   заголовком `X-Posta: 1` (посмотри, как это сделано в `js/content.js` около строки 515). Итог — строка статуса
   «Добавлено в подборку: N» + ошибки списком (`textContent`).

**Проверка** (CLI): вызови функции напрямую с тестовым пользователем. Создай тестового пользователя через
`app_user_bind`/способ, которым это делают другие `tools/test_*.php` (посмотри `tools/test_media.php`), положи
в пул 2 картинки (сгенерируй их через GD: `imagecreatetruecolor` + `imagepng`) и CSV из 3 строк (одна с битой
ссылкой). Проверь: в пуле 2 + 2 записи, у них есть `image_local`, файлы есть на диске (`media_cache_path`),
ошибка по битой строке с её номером. Оформи проверку как `tools/test_own_content.php`. **Удали** тестового
пользователя, его пул и файлы после проверки.

## Часть E. Порядок в выдаче и жалобы

- Свой контент (`own = true`) в подборке показывается первым, но **не** влияет на рейтинги источников:
  найди, где голоса пользователя пишутся в статистику источников (`source_feedback_stats`, `combined_source_rates`,
  `screened_cache`) — записи с `source_key` `own:…` туда попадать не должны.
- Пункт меню «Ошибка подбора» (жалоба) для своего контента скрыт: подбора не было.
- Карточка показывает источник «Свой контент · <имя файла или CSV>»; ссылки «Открыть исходный пост» нет.

**Проверка:** `grep -rn "source_key" lib handlers | grep -v own` — просмотри места, где источник считается в
статистику, и убедись, что `own:` отфильтрован. Запусти `tools/test_own_content.php` снова: голос «нравится» по
своей записи не меняет `source_feedback_stats(<тема>)`.

## Часть F. API (только если A–E прошли)

Пост в подборку из внешних сервисов (Make, n8n, свой скрипт).

- Токен: в «Личном кабинете» кнопка «Создать токен API» → сервер генерирует `bin2hex(random_bytes(24))`, хранит
  **только** `hash('sha256', $token)` в `kv` под ключом `'api_token:' . $uid`, а сам токен показывает один раз.
  Повторное создание заменяет старый. Кнопка «Отозвать».
- Действие `api_add_post`: `POST index.php?action=api_add_post`, заголовок `Authorization: Bearer <токен>`, тело JSON
  `{"text": "...", "image_url": "https://..."}` или `{"text": "...", "image_urls": [...]}` (до 10).
  - Это **единственное** действие, которое работает без куки и без `X-Posta`: найди в `index.php`, где
    проверяется `X-Posta` и где определяется пользователь по куке, и сделай исключение строго для этого
    действия. Пользователь определяется по токену (поиск `kv` по хэшу); неверный токен → 401 без подробностей.
  - Картинки — `fetch_public_image()`, дальше тот же `own_post_item` (`label` = «API»).
  - Лимит: не больше 60 запросов в час на пользователя (счётчик в `kv`), сверх — 429.
- В кабинете под токеном — пример `curl` с подставленным адресом сайта (`location.origin`).

**Проверка:** `tools/test_own_content.php` дополни: создать токен тестовому пользователю, вызвать обработчик
`api_add_post` так, как его вызовет `index.php` (подставь `$_SERVER['HTTP_AUTHORIZATION']`), проверить, что
пост в пуле; неверный токен → 401; 61-й запрос → 429. Удали тестовые данные.

## Итоговая проверка

- `php -l` по всем изменённым и новым PHP; `node --check` по изменённым JS.
- `grep -rn "http_get(" lib/rss.php lib/own_content.php` — пусто.
- `grep -rn "LIBXML_NOENT" lib` — пусто.
- Каталог не потерял записи: число строк `catalog_sources` по каждому `kind` совпадает с числом **до** твоей работы
  (запиши его в части 0), кроме удалённых тестовых.
- Тестовых данных нет: ни темы `тест-источники` в каталоге, ни тестового пользователя.

## Что сдать

Короткий отчёт пользователю по-русски:
- список изменённых и новых файлов (одна строка на файл: что в нём поменялось);
- какие RSS-ленты проверены и сколько постов с картинками они дали;
- что пользователь проверяет сам: «Источники» → «Добавить источник» (t.me, vk.com, адрес сайта) → источник появился,
  в следующем подборе есть его посты; «Свой контент» → загрузить 2 картинки → они первые в подборке; CSV-образец;
- Instagram пропущен — одна строка почему;
- ограничения: файлы до 2 МБ (настройка сервера), видео только через редактор поста;
- что не сделано и почему.
