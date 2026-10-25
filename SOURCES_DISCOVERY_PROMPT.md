# Задача: автоподбор источников для всех площадок, а не только Telegram

Ты работаешь в PHP-проекте `/var/www/html/posta`. Площадки-источники уже умеют **читаться** (Telegram, VK, RSS с
YouTube / YouTube Shorts / Pinterest / Tumblr, Одноклассники, TikTok, Двач — см. `SMMBOX_SOURCES_PROMPT.md` и
`TIKTOK_X_SHORTS_PROMPT.md`), но **искать** по теме автоподбор умеет только Telegram и VK.

Владелец: «не ищутся источники TikTok; проверь остальные; почему в выпадающем списке вкладки „Источники“ их так мало».

## Что выяснено (2026-10-23, перепроверять не нужно)

**Каталог сейчас:** Telegram 29, VK 6, RSS 0, Одноклассники 0, TikTok 0. Находки автоподбора (`source_suggestions`):
133, **все — Telegram**.

**Почему так:**
- `run_source_discovery_locked()` (`lib/discovery.php`) ищет кандидатов только так: поиск Telegram через аккаунт
  пользователя, «похожие» Telegram, поиск VK (`vk_search_groups`), ссылки t.me из постов, каналы, которые «знает»
  модель. `check_discovery_candidate()` понимает только Telegram и VK.
- **VK не подключён** (`vk_is_connected()` = false) → поиск VK молча пропускается. `groups.search` требует
  пользовательский токен VK — без OAuth не обойтись.
- TikTok, Одноклассники, RSS, YouTube, Pinterest, Tumblr попадают в каталог **только ручным добавлением по
  ссылке**; на вкладке «Источники» для них `catalog_source_candidates()` ищет в пустом каталоге → «Новых
  неоценённых источников на этой платформе пока нет».
- **Выпадающий список** `#source-platform` (`index.html:187`, `SOURCE_PLATFORM_FILTERS` в `handlers/sources.php:4`):
  Все, Telegram, ВКонтакте, RSS и сайты, Одноклассники, TikTok, Двач. **YouTube, YouTube Shorts, Pinterest, Tumblr
  в нём нет** — они хранятся как RSS (`rss_feed_kind()` различает их по адресу ленты), но отдельно не фильтруются.

**Что работает с этого сервера (проверено):**
- **YouTube — поиск каналов через yt-dlp работает:**
  `python3 -I tools/bin/yt-dlp --no-warnings --js-runtimes node:/usr/bin/node --flat-playlist --playlist-end 10
  --print "%(channel)s|%(channel_id)s|%(url)s" "https://www.youtube.com/results?search_query=<запрос>&sp=EgIQAg%253D%253D"`
  (фильтр «только каналы») → «КОТЯТА ПОЯСНЯЮТ UCkxVk…», «Kote Kitty Shorts UCNWbg…», «Simon's Cat» и т.д.
  `"ytsearch10:<запрос> shorts"` → ролики с `channel_id` и `view_count` — так находятся каналы шортсов.
- **TikTok — yt-dlp искать не умеет** (`tiktoksearch` нет; `tiktok.com/tag/…` → «No working app info is available»).
  **Работает headless Chrome:** `google-chrome --headless=new --disable-gpu --user-data-dir=<каталог>
  --virtual-time-budget=15000 --user-agent="<обычный Chrome>" --dump-dom "https://www.tiktok.com/search/user?q=<запрос>"`
  → ~620 КБ HTML, в нём `href="/@kote_kitty"`, `/@kotiki_kk`, `/@cats_house39`… (9 аккаунтов по «котики»).
  Хрупко: TikTok может начать показывать капчу; тяжело для слабого сервера (Chrome, ~15 с).
- **Одноклассники — поиск групп требует входа** (`ok.ru/search?st.mode=Groups…` и мобильный — без результатов,
  «Войдите»). **Но есть общие ленты разделов ОК:** `https://ok.ru/atom-feed/hobby` (100 записей) — в ней ссылки
  `ok.ru/group/<id>` групп-авторов (12+ разных групп). Видел ещё `atom-feed/collection`. Другие разделы — проверь сам
  (`https://ok.ru/atom-feed/<раздел>`), они не по темам, а общие — годятся как «улов» для проверки моделью.
- **Ссылки из уже найденных источников:** посты и описания Telegram-каналов темы часто ссылаются на
  `tiktok.com/@…`, `youtube.com/@…`, `ok.ru/group/…`, `vk.com/…`, сайты. Сейчас собираются только ссылки `t.me`
  (`telegram_linked_channels`).
- Reddit, Дзен, поисковики (DuckDuckGo, Bing) — с сервера не работают, не трогай.

## Что делаем

Части по порядку: 0 → A → B → C → D → E. После каждой — проверка. Не начинай следующую, пока не прошла предыдущая.

## Общие правила

- **Не делай git commit.** Перед записью в базу: `cp data/posta.sqlite data/posta.sqlite.bak-discovery2`.
- **НЕ открывай сайт Posta** (`http://localhost/...`): каждая загрузка без куки создаёт пользователя и запускает
  парсер, который нельзя остановить без sudo. Сторонние сайты (TikTok, YouTube, ОК) открывать можно.
- **Модели:** оценка кандидатов — существующими функциями (`evaluate_source` и т.п.), новых вызовов моделей не
  добавляй. В проверках — не больше 10 кандидатов на тему.
- **Каталог общий для всех.** В проверках — тема `тест-котики` (и только она), после — убери её записи из каталога,
  предложений, голосов и `discovery.checked_at`.
- Запуск внешних программ (yt-dlp, Chrome) — только `proc_open` с **массивом** аргументов, с таймаутом; запрос
  пользователя/темы — отдельным аргументом, в адресе — `rawurlencode`. Для yt-dlp — существующая `ytdlp_run()`.
- Код в стиле проекта: короткие функции, комментарии по-русски одной строкой, `RuntimeException` с понятным текстом.

## Часть 0. Прочитай

`lib/discovery.php` целиком; `lib/sources.php` (константы площадок, `CATALOG_KEYS`, `source_id_for`,
`telegram_linked_channels`, `telegram_channel_info`); `lib/ytdlp.php`; `lib/tiktok.php`; `lib/ok.php`; `lib/rss.php`
(`rss_known_feed`, `rss_feed_kind`); `lib/source_add.php` (`source_parse_link`, `source_probe` — как проверить
источник любой площадки); `handlers/sources.php` целиком; `js/sources.js` (фильтр площадок, карточка кандидата,
метка площадки); `#source-platform` и окрестности в `index.html`; `config.php` — `DISCOVERY_*`, `SOURCE_DISCOVERY_*`.

## Часть A. Выпадающий список и честные сообщения

1. В `SOURCE_PLATFORM_FILTERS` и `#source-platform` добавь **YouTube**, **YouTube Shorts**, **Pinterest**,
   **Tumblr** — это подвиды RSS: фильтр = `PLATFORM_RSS` + `rss_feed_kind()` (`YouTube`, `YouTube Shorts`,
   `Pinterest`, `Tumblr`). «RSS и сайты» показывает только обычные ленты (вид `RSS`). Порядок: Все, Telegram,
   ВКонтакте, TikTok, YouTube, YouTube Shorts, Одноклассники, Pinterest, Tumblr, RSS и сайты, Двач.
   Фильтр применяется и к каталогу, и к находкам автоподбора (`suggestion_source_candidates`).
2. Сообщения при пустом результате — честные и с действием, по площадке:
   - VK не подключён — «Подключите VK: без этого паблики не ищутся» (кнопка подключения уже есть — сошлись на неё);
   - площадки без автопоиска (Pinterest, Tumblr, RSS) — «Сами не находятся: добавьте ссылку в „Добавить источник“»;
   - площадки с автопоиском (после частей B–D) — «Пока ничего нового. Автоподбор ищет и здесь — загляните позже»
     и кнопка «Искать сейчас» (заказ автоподбора, `request_source_discovery()` — как существующая кнопка).
3. В карточке кандидата метка площадки — с видом (`kind`): «YouTube Shorts», «Pinterest», а не «RSS».

**Проверка:** `php -r` — `handle_suggest_sources(['platform' => 'youtube'])` (через заглушки `json_response`, как
в `tools/test_own_content.php`) отвечает понятным сообщением; `node --check js/sources.js`.

## Часть B. Каждая площадка — «искатель» с одним интерфейсом

Сейчас поиск — один длинный `run_source_discovery_locked`. Вынеси площадки в функции одного вида:
`discovery_find_<площадка>(array $queries, string $topic): array` → `[['platform', 'id', 'origin', 'info' => …|null]]`.
- `discovery_find_telegram`, `discovery_find_vk` — то, что есть сейчас (перенеси без изменения поведения);
- `check_discovery_candidate()` — обобщи на все площадки через `source_probe()` (или её часть без названия):
  посты, подписчики (если площадка отдаёт), активность; ярлык в журнале — по площадке (`@канал`, `vk.com/…`,
  `tiktok @…`, `youtube …`, `ok:<id>`). Логика оценки (`evaluate_source`, `source_quality_score`, мёртвые/неактивные)
  — общая. Подписчиков нет — аудитория не штрафуется (как у RSS).
- Порядок в прогоне: Telegram → VK → YouTube → TikTok → ОК → ссылки из найденных; общий лимит
  `SOURCE_DISCOVERY_MAX_CHECKS` делится так, чтобы каждая площадка с кандидатами получила хотя бы 4 проверки
  (константа `DISCOVERY_MIN_CHECKS_PER_PLATFORM`).

**Проверка:** прогон на `тест-котики` с лимитом 10 даёт в журнале тот же поиск Telegram, что и раньше
(сравни запросы и кандидатов), ничего не сломалось.

## Часть C. YouTube и YouTube Shorts

`discovery_find_youtube($queries, $topic)`:
- до 3 запросов за прогон: поиск каналов (`results?search_query=…&sp=EgIQAg%3D%3D`, до 10) и
  `ytsearch10:<запрос> shorts` (каналы авторов шортсов, без повторов);
- кандидат — лента канала: если у канала есть шортсы (лента `playlist_id=UUSH…` непустая) — лента шортсов (видео
  скачивается через очередь yt-dlp, как уже сделано), иначе обычная лента канала. Используй `rss_known_feed()`;
- `origin` — «поиск YouTube «запрос»»; каналы с < 3 роликов за `CONTENT_MAX_AGE_DAYS` — неактивны.

**Проверка:** на `тест-котики` — в журнале YouTube-кандидаты, хотя бы один добавлен или предложен; в выпадающем
списке «YouTube Shorts» он появляется. Убери тестовые записи.

## Часть D. TikTok и Одноклассники

1. **TikTok через headless Chrome** — `lib/tiktok_search.php`, `tiktok_search_users(string $query): array`
   (до 10 имён `@…`, только `is_valid_tiktok_user`):
   - `google-chrome --headless=new --disable-gpu --no-first-run --user-data-dir=<каталог данных>/chrome-tiktok
     --virtual-time-budget=15000 --user-agent=<строка обычного Chrome> --dump-dom https://www.tiktok.com/search/user?q=…`,
     таймаут 40 с; каталог профиля — в каталоге данных проекта, с правами как у остального (парсер и сайт работают
     от разных пользователей ОС — проверь, как сделано для `ytdlp-cache`);
   - разбор: `href="/@имя"`, без повторов, без `/@` пустого;
   - **одновременно — не больше одного Chrome на сервер** (`try_lock`), не больше 2 запросов за прогон;
   - капча/пусто → пустой список и строка в журнале «TikTok не отдал поиск (капча?)»; после 3 пустых подряд —
     пауза поиска TikTok на сутки (`kv`), чтобы не долбить.
   `discovery_find_tiktok` = поиск + ссылки `tiktok.com/@…` из найденных источников (пункт 3).
2. **Одноклассники** — `discovery_find_ok`: группы-авторы из общих лент разделов ОК (`atom-feed/hobby`,
   `atom-feed/collection` и другие разделы, которые найдёшь и которые отвечают 200 с записями) — до 15 групп за
   прогон, **сначала** фильтр ключевыми словами темы по заголовкам и тексту их записей в ленте (если у темы есть
   `keywords` в профиле — см. `TOPICS_SEARCH_PROMPT.md`; нет — по названию и синонимам темы), чтобы не гонять модель
   по каждой случайной группе. Плюс ссылки `ok.ru/group/…` из найденных источников.
3. **Ссылки на все площадки из найденных источников.** Обобщи `telegram_linked_channels` → `source_linked_sources($html|$posts)`:
   из описания и постов источника (Telegram, VK, RSS) достаёт ссылки на t.me, vk.com, tiktok.com/@, youtube.com/@ и
   /channel/, ok.ru/group, pinterest/tumblr — через `source_parse_link()` (без сетевых запросов на этом шаге: для
   ссылок, где `source_parse_link` ходит в сеть, — только распознавание формата). В очередь — как сейчас «упомянут в»,
   с той же защитой от ухода в сторону (если `TOPICS_SEARCH_PROMPT.md` выполнен — с `depth` и ключевыми словами).

**Проверка:** `php -r` — `tiktok_search_users("котики")` (1 вызов) возвращает имена; прогон на `тест-котики` с
лимитом 10: в журнале TikTok- и ОК-кандидаты с причиной отказа или принятия. Замерь время прогона и память Chrome
(`/usr/bin/time -v` на одном вызове) — в отчёт. Убери тестовые записи.

## Часть E. VK без пользовательского OAuth — только если есть законный способ

Проверь по документации VK API, есть ли метод поиска сообществ с сервисным ключом приложения (без входа
пользователя). **Если нет — ничего не делай**, только в отчёте: «VK ищется только после подключения VK; сейчас не
подключён» и где пользователь это делает. Не скрейпь `vk.com/search` и не используй чужие токены.

## Отчёт

1. Что сделано по частям, файлы.
2. Журнал прогона `тест-котики` (сокращённо): по площадкам — сколько найдено, проверено, принято.
3. Время и память поиска TikTok через Chrome; что делать, если TikTok начнёт показывать капчу.
4. Что пользователю проверить на вкладке «Источники»: новые пункты списка, сообщения, кнопка «Искать сейчас».
5. Что не сделано и почему (VK, Pinterest, Tumblr, RSS-сайты — без автопоиска).
