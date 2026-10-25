# Задача: догнать SMMbox по источникам — YouTube, Pinterest, Tumblr, Одноклассники, «лучшие посты», зеркало, папки и Яндекс Диск

Ты работаешь в PHP-проекте `/var/www/html/posta` (Posta: ИИ сам подбирает контент по теме и публикует в
Telegram-канал пользователя). Главный конкурент — SMMbox. Первая волна источников уже сделана по
`SOURCES_PROMPT.md`: добавление своей ссылки (Telegram, VK, сайт/RSS), «Свой контент» (файлы, CSV), API.
Эта задача — вторая волна: то, что у SMMbox есть, а у нас нет.

## Что есть у SMMbox (проверено 2026-10-23 по smmbox.com и их документации)

| У SMMbox | У нас сейчас | Что делаем |
|---|---|---|
| Поиск контента по группам VK, ОК, Facebook, Tumblr; по 30 постов из группы, сортировка по дате, лайкам, репостам, комментариям, «виральности»; фильтр по типу (фото, видео, опросы…) | Автоподбор моделью по теме: TG, VK, RSS, Двач | Часть C: «Лучшие посты источника» |
| «Поиск лучших постов» сообщества за период по ссылке | Нет | Часть C |
| Кросспостинг: следить за группой (VK, ОК, TG…) и сразу публиковать новые посты; фильтр по словам, автозамена (с regex), добавить текст в конец, ИИ-рерайт | Нет | Часть D: «Зеркало источника» (без ИИ) |
| RSS любого сайта, фильтр по словам | Есть (`lib/rss.php`) | Часть A: распознавание YouTube, Pinterest, Tumblr |
| Одноклассники как источник | **Баг:** ссылка на группу ОК превращается в чужую общую ленту | Часть A (починить), часть B (площадка ОК) |
| Папка до 1000 файлов, «облака» | До 10 файлов за загрузку | Часть E: папка и Яндекс Диск |
| CSV, API (n8n, Zapier) | Есть | Ничего |
| Расширение браузера | Нет | Не делаем (отдельный продукт) |

Что я проверил с этого сервера 2026-10-23 (перепроверять не нужно, но если поведение другое — напиши в отчёте):
- `source_parse_link('https://ok.ru/group/53038939046008')` возвращает `RSS https://ok.ru/atom-feed/hobby`:
  `rss_discover` находит на странице группы **общую ленту раздела ОК**, а не ленту группы. Пользователь получит
  чужие посты. Это баг.
- `https://www.youtube.com/@YouTube` и `https://www.pinterest.com/pinterest/` → «Страница не загрузилась: Failure
  writing output…»: страница больше 1 МБ — лимита `rss_discover`. `https://staff.tumblr.com/` → «Страница не
  загрузилась».
- Прямые ленты читаются: `https://staff.tumblr.com/rss` — 5 постов с картинками; `https://www.pinterest.com/<user>/<board>.rss`
  — 5 постов, но картинки маленькие (`i.pinimg.com/236x/…`).
- `https://www.youtube.com/feeds/videos.xml?channel_id=UCBR8-60-B28hp2BmDPdntcQ` загружается, но `rss_fetch_posts`
  возвращает **0 постов**: превью лежит в `media:group/media:thumbnail`, а разбор ищет `media:thumbnail` прямо в `entry`.
- Страница группы `https://ok.ru/group/<id>` отдаётся без входа (около 480 КБ HTML): в ней посты (`topic/<id>`,
  `data-tid="…"`), картинки `i.okcdn.ru/i?r=…`, счётчики `widget_count js-count`. Мобильная версия
  `https://m.ok.ru/dk?st.cmd=altGroupMain&st.groupId=<id>` — около 44 КБ, её проще разбирать.
- Reddit (`/r/…/.rss`, `.json`) отвечает **403** — не делаем. Дзен — 302 без ленты — не делаем.
- `https://cloud-api.yandex.net/v1/disk/public/resources?public_key=…` доступен (для чужого ключа — 404 JSON).

**Не делаем и почему** (в отчёте — одной строкой каждое): Instagram и Facebook (Meta признана в РФ экстремистской,
читать без входа нельзя), TikTok, YouTube Shorts как видео и X/Twitter (отдельный промпт
`TIKTOK_X_SHORTS_PROMPT.md` после этого), Reddit (403 с сервера), Дзен (нет ленты), Google Диск (нужен ключ API — позже), ИИ-рерайт в зеркале (модель на 3 ГБ
видеокарты занята парсером), расширение браузера.

Делай части по порядку: 0 → A → B → C → D → E. Не начинай следующую, пока не прошла проверка предыдущей.
Части D и E — **только если A–C прошли полностью**; иначе остановись и напиши, на чём застрял.

## Общие правила

- **Не делай git commit.** Пользователь проверит изменения сам.
- **НЕ открывай сайт Posta** (`http://localhost/...`) в curl, headless Chrome или Playwright: каждая загрузка без
  куки создаёт нового пользователя и запускает парсер, который нельзя остановить без sudo. Сервер проверяй через
  `php` в командной строке, интерфейс — опиши в отчёте, что пользователю проверить глазами.
- **Не трогай `php.ini`, `crontab`, systemd, Apache, sudo.** Лимиты: `upload_max_filesize = 2M`, `post_max_size = 8M`.
- **Ollama не вызывай** — ни в новом коде, ни в проверках.
- В интерфейсе **не упоминай SMMbox** и другие сервисы (закон о рекламе: некорректное сравнение).
- Код в стиле проекта: короткие функции, комментарии по-русски одной строкой, ошибки — `RuntimeException` с
  понятным русским текстом, SQL — через `db()`, `db_one`, `db_all`, `db_exec`, `db_tx`. Обработчики тонкие, логика
  в `lib/`. Новые POST-действия — в список `POST` в `index.php`, GET — в `GET`.
- **Всё внешнее — чужие данные.** На странице только `textContent` / `escapeHtml`. Ссылки — только `https://`.
  Сетевые запросы к адресам от пользователя — **только** через `fetch_public()` (`lib/core.php`) или `rss_get()`
  (`lib/rss.php`), не `http_get()`.
- Вёрстка — существующие классы из `css/app.css` и тот же вид, что у блока «Добавить источник» и «Свой контент».
  Новых шрифтов, цветов и библиотек не вводи.
- Тесты — CLI-скриптом `tools/test_sources2.php` по образцу `tools/test_own_content.php` (тестовый пользователь,
  заглушки `json_response`, уборка за собой). Каждая часть добавляет свои проверки; в конце `exit($failures ? 1 : 0)`.

## Часть 0. Прочитай целиком

- `SOURCES_PROMPT.md` — как делалась первая волна и какие там правила.
- `lib/rss.php` целиком (`rss_get`, `rss_load`, `rss_kids`, `rss_item_media`, `rss_parse`, `rss_discover`).
- `lib/source_add.php` целиком (`source_parse_link`, `source_probe`, `source_add_manual`).
- `lib/own_content.php` целиком (`own_post_item`, `own_store_image`, `own_add_to_pool`, `own_upload`).
- `lib/sources.php`: строки 1–60, `fetch_telegram_channel_page($channel, $before)`, `telegram_page_engagement`,
  `telegram_engagement_snapshot`, `telegram_posts_from_html`, `vk_api_request`, `build_vk_preview_posts`,
  `build_source_preview_posts`, `source_preview_images`, `load_source_db`, `update_source_db`.
- `lib/parser.php`: `collect_catalog_source`, `collect_posts_by_platform`, `add_item_to_pool_safely`,
  `candidate_is_known`, `collect_known_images`, `run_collector` (где начинается проход пользователя).
- `lib/content.php`: `source_post_url`, `media_cache_store_image`, `media_cache_path`, `media_cache_cleanup`.
- `lib/telegram.php`: `outbox_queue_post` и вокруг (строки ~600–700) — как пост ставится в очередь публикации.
- `handlers/sources.php` целиком, `js/sources.js` целиком, разметку вкладки «Источники» в `index.html`,
  `js/core.js` (`safeSourcePostUrl`, `api()`, `el()`).
- `lib/db.php` — найди последний номер миграции.

## Часть A. Ссылки на YouTube, Pinterest, Tumblr и баг с ОК

Всё это — ленты RSS, новая площадка не нужна: в каталоге они лежат в `rss_feeds` с `PLATFORM_RSS`.

1. В `source_parse_link()` **перед** общей веткой `https?://` распознай ссылки и сразу верни адрес ленты (без
   `rss_discover`). Вынеси это в `rss_known_feed(string $url): ?string` в `lib/rss.php`:
   - Tumblr: `https://<name>.tumblr.com/…`, `https://www.tumblr.com/<name>`, `https://tumblr.com/<name>` →
     `https://<name>.tumblr.com/rss` (имя: `[a-z0-9-]{1,32}`, без `www`);
   - Pinterest (`pinterest.com`, `ru.pinterest.com`, `pinterest.ru` и т.п.): `/<user>/<board>/` →
     `https://www.pinterest.com/<user>/<board>.rss`; `/<user>/` → `https://www.pinterest.com/<user>/feed.rss`.
     Пины (`/pin/…`) и служебные пути (`/search/`, `/ideas/`, `/today/`) → `RuntimeException('Нужна ссылка на
     профиль или доску Pinterest')`. **Проверь** на живом профиле, что `feed.rss` действительно отдаёт посты;
     если нет — для профиля верни понятную ошибку «Вставьте ссылку на доску», а доски оставь;
   - YouTube: `/channel/UC…` → `https://www.youtube.com/feeds/videos.xml?channel_id=UC…`;
     `/@handle`, `/c/name`, `/user/name` → загрузи страницу через `rss_get($url, 3 * 1024 * 1024)` и найди
     `channel_id` (`<link rel="canonical" href="https://www.youtube.com/channel/UC…">` или `"channelId":"UC…"`;
     формат `UC[A-Za-z0-9_-]{22}`). Ссылки на одно видео (`watch?v=`, `youtu.be/`, `/shorts/`) → ошибка «Нужна
     ссылка на канал YouTube»;
   - Одноклассники (`ok.ru`, `m.ok.ru`): **пока** — `RuntimeException('Одноклассники скоро будут, пока добавьте
     другую ссылку')`. В части B это заменится на настоящую площадку. Главное — ссылка на ОК больше никогда не
     должна уходить в `rss_discover`.
2. В `rss_discover()` подними лимит загрузки страницы с 1 до 3 МБ (тяжёлые главные страницы сайтов). Эвристик
   «лента не та» не добавляй: ОК закрыт пунктом 1.
3. Разбор лент (`lib/rss.php`):
   - YouTube: ищи `media:thumbnail` и `media:description` **внутри `media:group`**. Картинка — превью
     (`https://i.ytimg.com/vi/<id>/hqdefault.jpg`, если в ленте другое — бери из ленты). Видео не скачиваем: `video`
     пустой, а в конец текста добавь строку со ссылкой на ролик (`https://www.youtube.com/watch?v=<id>`), чтобы в
     канале была ссылка. Шорты (`/shorts/` в ссылке) — так же;
   - Pinterest: картинку `i.pinimg.com/236x/…` замени на `i.pinimg.com/736x/…` (проверь, что такой адрес отдаётся;
     если нет — `originals`). Сделай это одной функцией `rss_upgrade_image_url()`, она же место для подобных правок.
4. Метка площадки для карточек и журнала: `rss_feed_kind(string $feed_url): string` → `YouTube` / `Pinterest` /
   `Tumblr` / `RSS` по хосту ленты. Используй её:
   - в `handle_topic_sources()` и `source_vote_record()` — поле `kind` в карточке (сама `platform` остаётся `RSS`);
   - в `js/sources.js:264` (`source-platform-tag`) — показывай `kind`, если он есть;
   - в названии источника: префикс `SOURCE_NAME_PREFIXES` остаётся `RSS | `, **не** меняй его (по нему
     `content_platform()` узнаёт площадку старых записей).
5. Подсказку под полем «Добавить источник» (`index.html`) дополни: «… · YouTube, Pinterest, Tumblr: ссылка на
   канал, доску или блог».
6. `source_post_url()` (`lib/content.php`) уже пускает ссылку записи, если её хост совпадает с хостом ленты.
   Проверь, что для YouTube (`youtube.com` ↔ `www.youtube.com`), Pinterest и Tumblr ссылки «Открыть исходный пост»
   проходят; если нет — поправь сравнение хостов, а не ослабляй проверку.

**Проверка:**
```bash
php -l lib/rss.php lib/source_add.php handlers/sources.php
php -r 'require "bootstrap.php";
foreach (["https://www.youtube.com/@YouTube", "https://www.youtube.com/channel/UCBR8-60-B28hp2BmDPdntcQ", "https://youtu.be/dQw4w9WgXcQ",
  "https://staff.tumblr.com/", "https://www.tumblr.com/staff", "https://www.pinterest.com/pinterest/official-news/", "https://www.pinterest.com/pinterest/",
  "https://ok.ru/group/53038939046008", "https://habr.com/ru/"] as $l) {
  try { $s = source_parse_link($l); $p = rss_fetch_posts($s["id"], 3); echo $l, " → ", $s["id"], " [", rss_feed_kind($s["id"]), "] ", count($p), " постов; ", $p[0]["image"] ?? "-", PHP_EOL; }
  catch (Throwable $e) { echo $l, " → ошибка: ", $e->getMessage(), PHP_EOL; } }'
```
Ожидаемо: YouTube и Tumblr и доска Pinterest — посты с картинками (у YouTube в тексте ссылка на ролик, у Pinterest
картинка `736x`); видео YouTube и ОК — понятные ошибки; обычный сайт работает как раньше. Добавь эти случаи в
`tools/test_sources2.php` (сетевые проверки помечай в выводе `[сеть]`).

## Часть B. Одноклассники — новая площадка

Чтение без входа и без ключей: HTML группы. (Официальный API ОК требует регистрации приложения и ключей — не
делаем; если HTML не разбирается надёжно, остановись и опиши в отчёте, что видишь, вместо хрупкого кода.)

1. `lib/sources.php`: `const PLATFORM_OK = 'Одноклассники';`, префикс `'Одноклассники | '`, `CATALOG_KEYS` →
   `'ok_groups'`, `source_id_for` → `ok:`, `is_valid_source_id` → `is_valid_ok_group($id)` (только цифры, 10–20 знаков).
   Добавь `ok_groups` во **все** места, где перечислены виды каталога: `grep -rn "rss_feeds" lib handlers tools` —
   каждое место, где есть `rss_feeds`, должно знать и `ok_groups` (нормализация, `load_source_db`,
   `update_source_db`, `lib/topics.php:69`, `lib/discovery.php:15`, `handlers/sources.php:204`, фильтр
   `SOURCE_PLATFORM_FILTERS` → `'ok'`). Проверь, что запись каталога не теряет строки (счёт `catalog_sources` по
   `kind` до и после `update_source_db(fn($db) => $db)`).
2. Новый файл `lib/ok.php` (в `bootstrap.php` — после `rss`):
   - `ok_group_id(string $url): string` — из `ok.ru/group/<digits>`, `ok.ru/group<digits>`, `m.ok.ru/group/<digits>`
     — цифры сразу; из `ok.ru/<slug>` — загрузи страницу и найди id группы (`groupId,<digits>` в `data-l`, или
     `st.groupId=<digits>`). Не группа (профиль `ok.ru/profile/…`, видео, `ok.ru/dk?…` без группы) → ошибка
     «Нужна ссылка на группу Одноклассников»;
   - `ok_fetch_posts(string $group_id, int $count): array` — посты в том же формате, что `build_vk_preview_posts`
     (`image`, `images`, `text`, `source`, `post_id`, `posted_at`, `url` = `https://ok.ru/group/<id>/topic/<post_id>`),
     плюс `likes`, `comments`, `reshares` (целые или `null`). Выбери источник HTML сам: мобильная или полная версия
     — та, что стабильнее даёт картинку, текст, дату и счётчики. Картинки — только `https://i.okcdn.ru/` и
     `https://*.mycdn.me/`. Посты без картинки пропускай. Дату «вчера», «12 окт», «12 окт 2025», «14:05» переводи
     в unix-время (часовой пояс — `date_default_timezone_get()`); не разобрал — `0`;
   - `ok_group_title(string $group_id): string` — название из `og:title` / `<title>`, иначе id.
   - Все загрузки — через `fetch_public` (адрес строится из id, но правило одно для всех).
3. Подключи площадку везде, где есть RSS: `source_parse_link` (ветка ОК вместо временной ошибки из части A),
   `source_probe`, `collect_catalog_source` (подписчиков нет — `0`, `$rate = null`; ярлык в журнале — `ok:<id>`),
   `collect_posts_by_platform`, `build_source_preview_posts`, `handlers/sources.php` (`build_source_preview`,
   описание кандидата «Открытая группа в Одноклассниках с картинками в постах.», `source_vote_record`),
   `is_trusted_source_url` / `TRUSTED_SOURCE_HOSTS` и `safeSourcePostUrl` в `js/core.js` (хост `ok.ru`),
   `js/sources.js` (метка `ОК`), подсказка под полем «Добавить источник».
4. В `js/sources.js` фильтр площадок (если он рисуется списком) — добавь «Одноклассники».

**Проверка:** найди 2 живые открытые группы ОК с картинками (например, через страницу
`https://ok.ru/group/53038939046008` и ссылки на группы на ней), затем:
```bash
php -l lib/ok.php lib/sources.php lib/parser.php lib/source_add.php handlers/sources.php
php -r 'require "bootstrap.php"; $id = ok_group_id("<ссылка>"); echo $id, " ", ok_group_title($id), PHP_EOL;
foreach (ok_fetch_posts($id, 5) as $p) echo date("c", $p["posted_at"]), " | ♥", $p["likes"] ?? "-", " | ", mb_substr($p["text"], 0, 50), " | ", $p["image"], PHP_EOL;'
php -r 'require "bootstrap.php"; echo json_encode(source_add_manual("<ссылка>", "тест-ок"), JSON_UNESCAPED_UNICODE), PHP_EOL;'
```
Даты не нулевые хотя бы у большинства постов, картинки открываются (`curl -sI` по адресу картинки — 200,
`image/*`). Посты попадают в `collect_posts_by_platform(load_source_db(), "тест-ок")`. **Удали тестовую запись** из
каталога. Добавь проверки в `tools/test_sources2.php`.

## Часть C. «Лучшие посты источника»

Аналог «Поиска лучших постов» у SMMbox: пользователь вставляет ссылку, выбирает период и сортировку, видит сетку
лучших постов и забирает выбранные к себе в подборку. Модель **не** вызывается — пользователь выбирает сам.

1. `lib/best_posts.php` (в `bootstrap.php`):
   - `best_posts(string $link, int $days, string $sort, string $type = 'any', string $phrase = ''): array`.
     Ссылка разбирается `source_parse_link()`. Сбор постов за `$days` (1, 7 или 30):
     - Telegram: страницы `fetch_telegram_channel_page($id, $before)` назад, пока посты новее периода, но не
       больше 5 страниц; счётчики — `telegram_page_engagement` (просмотры, сумма реакций);
     - VK: `wall.get` с `count=100` и `offset` (не больше 3 запросов); `likes.count`, `reposts.count`,
       `comments.count`, `views.count`. Сейчас `build_vk_preview_posts` выбрасывает счётчики — добавь их в пост
       (`likes`, `reposts`, `comments`), не ломая остальных вызовов;
     - Одноклассники: `ok_fetch_posts` (одна страница — сколько отдаёт);
     - RSS / YouTube / Pinterest / Tumblr: счётчиков нет — сортировка только по дате (в ответе `sorts` это видно).
   - Сортировки: `date`, `likes` (ВК/ОК — лайки, ТГ — реакции), `reposts`, `comments`, `views`, `viral`.
     **Виральность** — насколько пост лучше обычного поста этого же источника:
     `(лайки + 2·репосты + комментарии) / медиана того же по собранным постам`; для Telegram —
     `(просмотры / медиана просмотров + реакции/просмотры ÷ медиана этой доли) / 2`. Медиана 0 → считай 1.
     Вынеси в `best_posts_virality(array $posts): array` (добавляет поле `viral`), это проверяется отдельно.
   - Фильтры: `type` = `any` / `photo` / `album` (несколько картинок) / `video`; `phrase` — подстрока в тексте без
     учёта регистра (`mb_stripos`).
   - Ответ: `['source' => [...название, площадка], 'sorts' => [доступные], 'posts' => [до 30 постов]]`, у каждого
     поста — `image`, `images`, `video`, `text` (первые 300 знаков), `url`, `posted_at`, счётчики, `viral`, и
     `token` — подписанный ключ поста (HMAC от `source_key|post_id|image`, секрет — любой существующий секрет
     проекта; найди, чем подписываются другие токены), чтобы забрать можно было только то, что сервер показал.
   - Результат кэшируй в `kv` на 15 минут по ключу `best:<uid>:<sha1 параметров>`.
     Лимит: 30 запросов «Лучших постов» в сутки на пользователя (`kv_update`, как `source_add_quota_take`).
   - `best_posts_take(string $cache_key, array $tokens): int` — выбранные посты из кэша → в подборку пользователя.
     Картинки сначала в кэш медиа (`fetch_public_image` + `media_cache_store_image`), затем запись в формате парсера
     (`source`, `source_key`, `source_url`, `topic` = `current_topic()`, `relevance` = `rating` = 100,
     `picked` = true) через `own_add_to_pool()` (она кладёт в начало и пропускает уже известное). Не больше 20 за раз.
     Видео: если это Telegram/VK и видеофайл доступен тем же способом, что в парсере, — бери; нет — только картинка.
2. Обработчики `handle_best_posts($in)` (GET: `link`, `days`, `sort`, `type`, `phrase`) и `handle_best_posts_take($in)`
   (POST: `key`, `tokens[]`) в `handlers/sources.php`. Тема 18+ (`topic_save_denied(current_topic()) !== null`) →
   ошибка, как в `handle_add_topic_source`.
3. Фронт: на вкладке «Источники» новый блок «Лучшие посты» под «Добавить источник»: поле ссылки, период
   (День / Неделя / Месяц), сортировка (только из `sorts`), тип, поиск по фразе, кнопка «Найти». Сетка карточек:
   картинка (через существующий прокси/отображение картинок, как в карточках источников), счётчики, дата, кусок
   текста, чекбокс. Кнопки «Выбрать все» и «Забрать в подборку (N)». После успеха — «Добавлено в подборку: N».
   Текст постов — только `textContent`.

**Проверка:**
```bash
php -l lib/best_posts.php handlers/sources.php
php -r 'require "bootstrap.php"; $p = best_posts_virality([["likes"=>10,"reposts"=>0,"comments"=>0],["likes"=>10,"reposts"=>0,"comments"=>0],["likes"=>100,"reposts"=>5,"comments"=>3]]);
echo json_encode(array_column($p, "viral")), PHP_EOL;'   # третий пост заметно больше 1, первые два ≈ 1
```
В `tools/test_sources2.php`: тестовый пользователь, `best_posts` по публичному Telegram-каналу за неделю с
сортировкой `viral` (посты отсортированы по убыванию `viral`), `best_posts_take` двух постов → в пуле
пользователя 2 новые записи с картинками в кэше медиа; повторный `take` тех же — 0; подделанный токен — ошибка.
Если VK подключён (`vk_is_connected()`), то же по паблику VK; иначе — строка «VK не подключён, пропущено».

## Часть D. «Зеркало источника» (только если A–C прошли)

Аналог кросспостинга SMMbox: пользователь выбирает источник, и **каждый новый пост** из него без оценки моделью
попадает к нему — в начало подборки или сразу в очередь публикации канала. Это **личная** настройка, в общий
каталог не идёт.

1. Миграция (следующий номер в `lib/db.php`): таблица `mirrors` (`id`, `user_id`, `platform`, `source_id`, `name`,
   `channel_id` — канал пользователя или `NULL`, `mode` — `pool` / `queue`, `rules` — JSON, `last_post_key`,
   `last_checked_at`, `created_at`), индекс по `user_id`. Не больше 10 зеркал на пользователя.
2. `lib/mirror.php`:
   - `mirror_rules_apply(string $text, array $rules): ?string` — `null`, если пост отсеян. Правила:
     `include` (слова — хотя бы одно), `exclude` (ни одного), `replace` — пары `[из, в]`; пара с флагом `regex`
     — регулярное выражение (проверь его `@preg_match` при сохранении, ограничь длину 200 и время:
     `ini_set('pcre.backtrack_limit', …)` в пределах функции, ошибка → правило пропускается),
     `strip_links`, `strip_mentions`, `strip_hashtags`, `append` (текст в конец, до 500 знаков).
   - `mirror_collect_user($uid): array` — для каждого зеркала, проверенного больше 15 минут назад: новые посты
     (новее `last_post_key`, при первом запуске — только последние 3), правила, затем по `mode`:
     `pool` → как в части C (кэш медиа + `own_add_to_pool`); `queue` → `outbox_queue_post(...)` в ближайший
     свободный слот расписания канала (найди, как это делает ручная постановка в очередь, и вызови то же самое,
     а не копируй логику). Возвращает счётчики для журнала.
   - Вызов — в начале прохода пользователя в `run_collector()` (`cli_echo` в стиле остального журнала), с общим
     ограничением 20 секунд на все зеркала пользователя.
3. Обработчики: `mirror_list` (GET), `mirror_save`, `mirror_delete` (POST). Ссылка — через `source_parse_link`.
   Тема 18+ и каналы чужого пользователя — ошибка.
4. Фронт: блок «Зеркало» на вкладке «Источники»: список зеркал (название, площадка, режим, «последняя проверка»,
   «удалить») и форма: ссылка, режим («В подборку» / «Сразу в очередь канала <выбор канала>»), слова «только
   с» / «без», замены (строки «что → на что», галочка «регулярное выражение»), галочки «убрать ссылки»,
   «убрать @упоминания», «убрать #хэштеги», «добавить в конец».

**Проверка:** юнит-проверки `mirror_rules_apply` в `tools/test_sources2.php` (include/exclude, обычная и regex
замена, плохой regex не роняет, `append`, все `strip_*`); тестовый пользователь + зеркало публичного
Telegram-канала в режиме `pool` → `mirror_collect_user` добавил ≤ 3 записи, повторный вызов сразу — 0 (не прошло
15 минут); режим `queue` проверь **только** если у тестового пользователя можно создать фиктивный канал без
реальной отправки — иначе опиши в отчёте, что проверить руками. Ничего в реальные каналы не отправляй.

## Часть E. Папка и Яндекс Диск (только если A–C прошли)

1. **Папка.** В «Свой контент» кнопка «Выбрать папку» (`<input type="file" webkitdirectory multiple>`). Фронт берёт
   только картинки и видео по расширению, до 1000 файлов, и отправляет их **по одному** существующим
   `own_upload` (каждый запрос ≤ 2 МБ — укладывается в лимиты сервера), с прогрессом «Загружено 37 из 412» и кнопкой
   «Остановить». Файлы больше 2 МБ — пропускать с итоговым списком «не загружены (больше 2 МБ): …». Если рядом
   лежит `.txt` с тем же именем — его текст идёт подписью поста. Сервер не меняй, если `own_upload` уже это
   позволяет; суточный лимит загрузок — проверь, есть ли он, и если нет — добавь 1000 файлов в сутки.
2. **Яндекс Диск.** Поле «Публичная ссылка на папку Яндекс Диска» (`https://disk.yandex.ru/d/…`,
   `https://yadi.sk/d/…`). Сервер (`lib/own_content.php`, `own_import_yadisk(string $public_url): array`):
   - список: `https://cloud-api.yandex.net/v1/disk/public/resources?public_key=<url>&limit=100&offset=…`, через
     `fetch_public`, только `type=file` и `media_type=image` (видео — если размер ≤ лимита видео кэша медиа);
   - скачивание: `…/public/resources/download?public_key=<url>&path=<path>` → `href` → `fetch_public` (адрес
     `href` — внешний, проверка `fetch_public` обязательна);
   - не больше 100 файлов за импорт (остальное — «импортировано 100 из 340, нажмите ещё раз — продолжит»:
     запоминай смещение в `kv` по `yadisk:<uid>:<sha1 ссылки>`), лимит времени 50 секунд, каждый файл — через
     `own_store_image` + `own_post_item` + `own_add_to_pool`;
   - подпись — из `.txt` с тем же именем в той же папке, если есть.
   - Обработчик `handle_own_import_yadisk` (POST), в список `POST`.

**Проверка:** `php -l`; в `tools/test_sources2.php` — разбор ссылок Яндекс Диска (хорошие/плохие) и, если найдёшь
живую публичную папку с картинками, импорт 3 файлов тестовому пользователю (`[сеть]`). Папку проверить из CLI
нельзя — опиши в отчёте, что пользователю проверить в браузере.

## Отчёт

1. По частям: что сделано, какие файлы, результат проверок (вывод `php tools/test_sources2.php` целиком).
2. Что пользователю проверить глазами в браузере — по пунктам.
3. Что не сделано и почему (включая список «Не делаем» выше, одной строкой каждое).
4. Что заметил хрупкого: особенно HTML Одноклассников — какие селекторы, что сломается при смене вёрстки.
