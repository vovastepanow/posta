# Задача: убрать прогноз вкуса, слить «Фап контент» в «Порно», починить видео YouTube Shorts в превью

Ты работаешь в PHP-проекте `/var/www/html/posta`. Три независимые части — делай по порядку, после каждой проверяй.

## Общие правила

- **Не делай git commit.**
- Перед записью в базу: `cp data/posta.sqlite data/posta.sqlite.bak-fapmerge`.
- В базу пиши через PHP-функции проекта (`update_source_db`, `history_update`, `kv_update`, `tools/cli.php`), не сырым SQL,
  если функция есть. Не во время работы парсера (`pgrep -af parser` — должно быть пусто).
- Пиши код в стиле окружающего: короткие русские комментарии `/** … */`, без лишних абстракций.
- Не открывай приложение в headless-браузере без нужды: свежая загрузка страницы = новый пользователь + парсер,
  который не остановить. JS проверяй через `node --check` и существующие Node-тесты (`tools/test_post_view.mjs`).
- Синтаксис PHP: `php -l` на каждый изменённый файл.

---

## Часть A. Убрать прогноз вкуса пользователя (поле `rating` от нейросети)

### Что выяснено (перепроверять не нужно)

- В `config.php:56` `const PERSONAL_TASTE_ENABLED = false;` — выключено, потому что прогноз почти не отличал
  лайки от дизлайков (AUC 0.60). Сейчас `rating` у новых записей всегда `null`. Код — мёртвый, но ветвит самое
  нагруженное место (`lib/ai.php`).
- Вкус **остаётся** учитываться без нейросети — это **не трогать**: `card_score` (`lib/quality.php`),
  `source_user_mutes` / `user_source_rates` (`lib/sources.php`), `quality_platform_blocked`, коллекции истории
  `liked` / `disliked` / `complaints`, жалобы «ошибка подбора» в промпте (`topic_complaint_texts`).

### Что сделать (минимальный вариант — поле `rating` в данных остаётся, просто всегда `null`)

1. `config.php`: удалить `PERSONAL_TASTE_ENABLED` и `TASTE_EXAMPLES`. `TASTE_EXAMPLE_CHARS` **используется**
   обрезкой жалоб (`lib/topics.php:657-661`) — переименуй в `COMPLAINT_EXAMPLE_CHARS` (и в комментарии), значение 140.
2. `lib/ai.php`:
   - удалить функцию `history_taste_texts`;
   - `content_analysis_schema`: убрать строку с `rating`;
   - `content_analysis_prompt`: убрать `$taste`, вставку `', примеры'`, строку про `rating (0-100)` и цикл
     `ПОНРАВИЛОСЬ / НЕ ПОНРАВИЛОСЬ`. Параметр `$history` остаётся — он нужен для жалоб;
   - `salvage_analysis_json`: убрать проверку `PERSONAL_TASTE_ENABLED` и разбор `rating`; в результате `'rating' => null`;
     поправь докблок («годится, если relevance уже есть»);
   - разбор ответа (~строки 536 и 554): условие только `is_numeric($a['relevance'] ?? null)`, `'rating' => null`.
3. `lib/parser.php:702-703`: убрать `if (!PERSONAL_TASTE_ENABLED) $a['rating'] = null;` и хвост `прогноз оценки` в
   `$verdict` (оставь `"соответствие теме {$a['relevance']}%"`). В записи пула `'rating' => null`.
4. Поищи остатки: `grep -rn "PERSONAL_TASTE\|TASTE_EXAMPLE\|history_taste" --include=*.php --include=*.js .`
   (каталог `data/chrome-*` пропусти) — должно быть пусто.
5. **Не трогать**: `rating_assigned` в истории и его проверку в `handlers/content.php:568-570`, `js/content.js:136`,
   `js/tabs.js:661-664`, `js/account.js` («Оценка ИИ») — они корректно показывают `null`; `'rating' => 100` у своего
   контента и «Лучших постов»; `tools/stand/*`.
6. Кэш анализа (`analysis_cache`) **не сбрасывать** — поле `rating` в старых ответах просто игнорируется. Проверь,
   что чтение из кэша не требует `rating` (если требует — убери требование).

### Проверка

- `php -l` для всех изменённых файлов.
- `php tools/cli.php` — найди команду, которая печатает промпт полного анализа или прогоняет одну запись (если есть),
  и убедись, что в промпте нет «rating» и «ПОНРАВИЛОСЬ», а ответ разбирается.

---

## Часть B. «Фап контент» слить с «Порно»; поиск «фап контент» должен находить «Порно»

### Что выяснено

- Дерево тем: `lib/taxonomy/adult.php` — частные `adult.porn` («Порно», алиасы `порнуха`, `xxx`) и `adult.fap`
  («Фап контент», алиас `фап`). Засев в реестр — `topic_tree_seed()` (`lib/topics.php:251`), реестр лежит в таблице
  `topics` (через `load_source_db()['topics']` / `update_source_db`).
- В реестре: `t0fe5642e` «Фап контент» — `key: adult.fap`, **`manual: true`, `scope: broad`, `parent: null`**
  (пользователь когда-то двигал руками, засев её место не трогает); `tcf1e4489` «Порно» — `key: adult.porn`,
  `parent: t191a1b9b`.
- Поиск темы: `topic_resolve` → `topic_lookup` (точное совпадение `topic_norm` по названию и алиасам) → иначе модель
  (`topic_ai_classify`). Значит, достаточно, чтобы «фап контент», «фап» и т. п. были **алиасами «Порно»**.
- **Ловушка засева:** `topic_tree_find` ищет существующую тему по всем именам узла. Если добавить «фап контент» в
  алиасы `adult.porn`, пока в реестре ещё живёт `t0fe5642e`, засев может сопоставить узел «Порно» со старой темой
  «Фап контент». Поэтому **сначала** слей/удали старую тему в реестре, **потом** запускай засев.
- Ссылки на «Фап контент» в данных: 17 записей `history_items` (поле `topic` в `data`), 3 записи `history_items` с меткой
  `adult.fap` в `tags`, 7 записей `analysis_cache` с `adult.fap`, в `kv`: `discovery` (`plan.topic = "Фап контент"`)
  и, возможно, `hot_topics`. `pool_items`, `users.settings`, `screened_cache`, `source_verdicts` — чисто (перепроверь
  `users.settings` — там может быть текущая тема пользователя).
- Словарь хэштегов: `data/taxonomy/hashtags/adult.tsv` — 6 строк с `adult.fap` (откровенно, секси, горячо, …).
- Слово «фап» уже есть в `TOPIC_ADULT_WORDS` (`lib/discovery.php:9`) и тегах досок 2ch (`lib/sources.php:819`) — не трогать.

### Что сделать

1. `lib/taxonomy/adult.php`: удалить узел `adult.fap`. В `adult.porn` добавить алиасы: `фап контент`, `фап`,
   `фапконтент`, `фап-контент`, `контент для фапа`. `about` «Порно» расширь: «Реальные люди: видны гениталии или секс,
   либо откровенные фото и видео для возбуждения.» Комментарий в файле говорит «Ключи не меняем: на них ссылаются
   записи» — поэтому ниже записи переносим на `adult.porn` явно.
2. Миграция реестра (одним `update_source_db`, можно разовой командой в `tools/cli.php`, например `topic-merge <откуда> <куда>`
   — если похожей команды ещё нет, сделай общую, она пригодится):
   - в «Порно» (`tcf1e4489`) добавить алиасы старой темы и её название; объединить `profile.queries` и
     `profile.telegram` без дублей (это запросы автоподбора — полезны). `criteria/exclude` «Фап контента» **не**
     переносить: они требуют юмора/абсурда и испортят оценку «Порно»;
   - темам с `parent === 't0fe5642e'` (если есть) поставить `parent` = `tcf1e4489`-родитель или сам `tcf1e4489` —
     посмотри, что логичнее, и напиши в отчёте;
   - удалить `t0fe5642e`;
   - `topic_registry(true)`.
3. Перенести ссылки в данных (через функции проекта):
   - `history_items`: `topic` «Фап контент» → «Порно»; в `tags` `adult.fap` → `adult.porn` (без дублей);
   - `analysis_cache`: записи с `adult.fap` — проще удалить (пересчитаются), если ключ кэша зависит от темы;
     иначе заменить метку;
   - `kv discovery`: если `plan.topic` = «Фап контент» — сбросить план (пусть перестроится), `hot_topics` — пересчитать
     или оставить (обновится сам — проверь, как);
   - `users.settings`: текущая тема «Фап контент» → «Порно».
4. `data/taxonomy/hashtags/adult.tsv`: `adult.fap` → `adult.porn` в 6 строках (без дублей с уже существующими строками
   `adult.porn`). Проверь, нет ли собранного кэша словаря (`lib/hashtags.php` / `lib/taxonomy.php`), который надо
   перестроить.
5. Запусти засев (`topic_tree_seed`, найди команду в `tools/cli.php`) и убедись по счётчикам, что ничего не создано
   заново с названием «Фап контент».

### Проверка

```
php -r 'require "bootstrap.php"; foreach (["фап контент","Фап контент","фап","фапконтент","порно"] as $q) { $e = topic_lookup($q); echo $q, " → ", $e["title"] ?? "null", "\n"; }'
```
Все пять → «Порно». Также `topic_resolve("фап контент")` → «Порно», `created: false`, `ai: false` (модель не зовётся).
`grep -rn "adult.fap" lib data/taxonomy` — пусто. `sqlite3 data/posta.sqlite "select count(*) from history_items where data like '%Фап контент%'"` → 0.

---

## Часть C. Видео YouTube Shorts в окне поста превью источника — «Смотреть на сайте ↗» вместо ролика

### Что выяснено

- Скриншот: вкладка «Источники», кандидат «RSS | Галимотья Приколов» (YouTube Shorts, найден автоподбором), открыт
  «Пост 1 из 15» — вместо видео тёмная плитка «Смотреть на сайте ↗» (ссылка `https://www.youtube.com/shorts/tfbnG3nCUeo`).
- Цепочка: `rss_fetch_posts` (`lib/rss.php:173-175`) даёт шортсам `video_via = ytdlp`, `video_page = …/shorts/<id>`;
  `source_preview_media` (`handlers/sources.php:127`) превращает это в `{kind: video, src: '', page: video_page,
  pending: video_pending}`; `video_preview_prepare` (`lib/ytdlp.php:330`) ставит в очередь **только первые
  `VIDEO_PREVIEW_FIRST = 3`** ролика (и молча ничего не делает, если `video_fetch_enqueue` вернул `limited` —
  лимит `VIDEO_PREVIEW_LIMITS['ytdlp'] = 3` задачи на пользователя).
- В просмотрщике `js/post-view.js:207-215` (`tileNode`): если `pending` — плитка «Видео загружается…» со слежкой
  (`pendingTile` → `pageCheck` → `video_status`); если **не pending, но есть `page`** — просто ссылка
  «Смотреть на сайте ↗». **Кнопки/автозапуска скачивания здесь нет** (в старом `js/viewer.js:106-130` `offerVideo`
  такая кнопка есть — через `api('video_fetch', {body: {url}})`).
- Для `tfbnG3nCUeo` в `kv` нет `video_preview:<хэш>` вообще — задача на скачивание даже не ставилась. Очередь
  `ytdlp_queue` пуста, воркер не запущен. Остальные 11 состояний — `done`.
- yt-dlp сам по себе этот ролик видит: `tools/bin/yt-dlp -s "https://www.youtube.com/shorts/tfbnG3nCUeo"` → формат
  `399+251`, ~0,5 МБ (есть предупреждение про JS-runtime — его не трогай, `ytdlp_exec` и так задаёт Node).
  Формат для YouTube в `ytdlp_format_for` — `bv*[vcodec^=avc1][height<=1280]+ba[ext=m4a]/b[ext=mp4]`.

### Что сделать

1. **Просмотрщик сам запускает скачивание.** В `js/post-view.js`: плитка видео с `page`, для которого
   `videoPageFor(page)` (из `js/viewer.js`) не пустой, и без `src`, без `pending`, без `unavailable`, — не ссылка,
   а `pendingTile`, перед слежкой вызывающая `api('video_fetch', { body: { url } })`:
   - ответ `done` с `local` — сразу видео-плитка;
   - `queued`/`running` — обычное ожидание (`watchVideo(pageCheck(...))`);
   - ошибка 429 (лимит) — плитка-кнопка «▶ Загрузить видео» (повторный клик снова зовёт `video_fetch`), под ней мелко
     «Смотреть на сайте ↗»;
   - ошибка 409 (Telegram без личного аккаунта) — как сейчас, ссылка + текст `TGU_NO_ACCOUNT_TEXT` из ответа.
   Скачивание запускай, **только когда пост реально показан** (лист открыт), а не при построении всех 15 листов —
   иначе упрёмся в лимит 3. Если `watchPending === false` (место вызова следит само), делай как раньше.
   Одна задача на адрес уже гарантирована сервером (`video_fetch_enqueue`), повторные вызовы безопасны.
2. `pageCheck` (`js/video-pending.js:55`): статус `none` сейчас = «Задачи скачивания нет: запустите загрузку заново.»
   После п.1 оставь, но плитка при этой ошибке должна показывать кнопку «▶ Загрузить видео», а не мёртвый текст.
3. Сервер: в `video_preview_prepare` при `limited` ничего не меняй, но убедись, что такие посты приходят с
   `pending: false` и `page` — тогда сработает п.1.
4. Выясни, почему для первого поста этого кандидата задача не поставилась (нет `kv video_preview:…`). Возможные
   причины: кандидат пришёл путём, который не вызывает `video_preview_prepare` (проверь все места, откуда в
   `js/sources.js` берутся посты кандидата: `suggest_sources`, в т. ч. `suggested_only`, `discover_sources`,
   сохранённое состояние на клиенте); или `limited` из-за зависших задач. Если путь без `prepare` есть — добавь вызов.
   Опиши в отчёте, что нашёл.
5. Проверка без браузера: `php -r` с `bootstrap.php` — вызови `video_fetch_enqueue('https://www.youtube.com/shorts/tfbnG3nCUeo', 'ytdlp', <uid>)`
   (uid возьми любого существующего из `users`), дождись воркера (`kv ytdlp_queue` опустеет), `video_preview_status(...)` →
   `done`, файл в `media_cache_path(local)` есть и это H.264 (`ffprobe`). JS — `node --check js/post-view.js js/video-pending.js`
   и `node tools/test_post_view.mjs` (допиши тест: видео с `page` шортса без `pending` → не ссылка).

---

## Отчёт в конце

Коротко по-русски: что изменено по частям A–C (файлы), что перенесено в данных (числа), причина из C.4,
что не сделано и почему. Бэкап базы — `data/posta.sqlite.bak-fapmerge`.
