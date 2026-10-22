# Задача: починить «Радар лидов»

Ты работаешь в PHP-проекте `/var/www/html/posta` (Postä: ИИ подбирает контент по теме и публикует в Telegram/VK).
Вкладка «Радар лидов» должна находить в Telegram-чатах людей, которые ищут решение для подбора контента или
автопостинга. Найденные сообщения классифицирует локальная модель Ollama, а лиды показываются во вкладке. Сейчас
радар не работает **совсем**: цепочка оборвана в пяти местах.

## Что сломано (проверено, перепроверять не нужно)

1. **Вкладку не видно.** В `index.html` у `<div id="telegram-leads-tab" class="view-panel wide-panel" hidden>` стоит
   атрибут `hidden`. В CSS есть `[hidden] { display: none !important }`, а `switchTab` в `js/core.js:255` только
   переключает класс `active`. Поэтому при клике на «Радар лидов» открывается пустое место.
2. **Очередь никто не наполняет.** `addToLeadQueue()` в `lib/ollama_classifier.php` нигде не вызывается: сообщения из
   чатов никто не читает. Помощник `tools/tg_user.py` (Telethon, личный аккаунт пользователя) умеет искать каналы и
   планировать посты, но читать сообщения не умеет. В таблицах `telegram_lead_queue` и `telegram_leads` 0 строк.
3. **Воркер не запущен и устроен неправильно.** `tools/ollama_worker.php` — бесконечный цикл `while (true)`, а README
   советует запускать его из cron каждую минуту, то есть каждую минуту стартовал бы ещё один вечный процесс. Сейчас
   он не запущен вовсе, в cron его нет.
4. **Классификатор не может ответить.** `classifyWithOllama()` шлёт в `my-model:latest` поле `system`. Но шаблон этой
   модели — голый `TEMPLATE {{ .Prompt }}`, поэтому системный промпт с инструкцией «ответь 1 или 0» модель не
   получает. `temperature` передаётся вне `options` и игнорируется, таймаут 5 секунд, а 9B-модель на этом сервере
   работает в основном на CPU. Ответы не парсятся либо обрываются по таймауту.
5. **Нет разделения по пользователям, IDOR.** `handle_telegram_leads` и `handle_telegram_leads_stats` отдают лиды
   **всех** пользователей. `handle_update_telegram_lead` меняет статус любого лида по id. Плюс путаница с полем
   `user_id`: воркер пишет туда то владельца, то автора сообщения (`https://t.me/$userId`).

И на фронтенде: если лидов нет, вечно крутится «Загрузка лидов…». Опрос идёт каждые 5 секунд с момента загрузки
страницы, даже когда вкладка радара закрыта.

## Как должно работать после починки

```
Пользователь в радаре: список чатов (@имя или t.me/...) + «Радар включён»
        │  раз в 10 минут, пока открыта вкладка приложения (и из cron, если пользователь его добавит)
        ▼
php tools/cli.php leads-scan <uid>      ← один проход, под блокировкой: два прохода одновременно не идут
  1. tg_user.py action=read_chats: новые сообщения из этих чатов (после последнего виденного id)
  2. отсев без модели: короткие, от ботов, свои, без ключевых слов → не в очередь
  3. остальное → telegram_lead_queue (INSERT OR IGNORE, дубли невозможны)
  4. до 15 сообщений из очереди → модель → telegram_leads, если это лид
        ▼
Вкладка «Радар лидов» показывает лиды ТОЛЬКО этого пользователя
```

Делай части по порядку: 0 → A → B → C → D → E → F → G. Не начинай следующую, пока не прошла проверка предыдущей.

## Общие правила

- **Не делай git commit.** Пользователь проверит изменения сам.
- **НЕ открывай сайт** (`http://localhost/...`) в curl, headless Chrome или Playwright. Каждая загрузка страницы без
  куки создаёт нового пользователя и запускает парсер, который нельзя остановить без sudo. API проверяй только
  через `php` в командной строке (см. проверки), интерфейс — глазами пользователя (опиши, что ему проверить).
- **Ollama на этом сервере — узкое место.** Видеокарта на 3 ГБ, 9B-модель работает в основном на CPU, и ту же
  Ollama постоянно использует парсер пользователя. Поэтому:
  - не загружай вторую большую модель. Классифицируй той, что уже используется приложением (`SMART_MODEL` из
    `config.php`, сейчас `qwen3.5-vision:latest`). Модель `my-model:latest` больше не используй: у неё сломан
    шаблон, и при загрузке она вытеснит основную модель из памяти;
  - **не переходи на маленькие модели (2B и т.п.) ради скорости** — это решает только пользователь;
  - в проверках делай не больше 4–5 запросов к модели.
- **Не трогай `crontab`, systemd и sudo.** Строку для cron только покажи пользователю в отчёте.
- Удалённый файл `migrations/009_create_telegram_leads.sql` не восстанавливай: схема живёт в миграциях `lib/db.php`.
- Код в стиле проекта: короткие функции, комментарии по-русски одной строкой, ошибки — исключениями
  (`RuntimeException` с понятным русским текстом), SQL — через `db()`, `db_one`, `db_exec`, `db_tx`.
- Всё, что приходит из Telegram (текст, имена, username), — чужие данные. На странице только через `textContent` или
  `escapeHtml`. В ссылки попадают только URL, собранные сервером по шаблону (`https://t.me/...`).

## Часть 0. Прочитай целиком

- `lib/ollama_classifier.php`, `tools/ollama_worker.php`, `TELEGRAM_LEADS_README.md`.
- `handlers/content.php` — функции `handle_telegram_leads`, `handle_update_telegram_lead`,
  `handle_telegram_leads_stats` (около строки 674).
- `index.php` — списки `GET`/`POST`, `ROUTE_HANDLERS` и порядок вызовов внизу.
- `lib/db.php` — `db_migrate` и миграции. Найди **последний** номер миграции: новая будет следующей. Сейчас последняя
  9, но проверь сам. Миграция 6 — таблицы радара.
- `lib/db_access.php` — `kv_get`, `kv_update`, `connections_load`.
- `lib/telegram.php` — раздел «Личный аккаунт»: `telegram_user_run`, `telegram_user_connection`,
  `telegram_user_call`, `telegram_user_find_channels` (как сделан вызов помощника).
- `tools/tg_user.py` **целиком** (292 строки): как устроены `run()`, `find_channel()`, `search_channels()`, как
  возвращается `session`.
- `lib/ai.php` — `ollama_stream`, `ollama_quick_json`, `decode_model_json`, `ollama_common_payload`. Обрати
  внимание: `ollama_stream` обрывает запрос, если `parser_session_alive()` вернул false. Найди эту функцию и пойми,
  что она вернёт в командной строке.
- `tools/cli.php` — как устроены команды и справка.
- `handlers/content.php:312` — как запускается фоновая команда (`exec('/usr/bin/php ... &')`).
- Обработчик `tab_heartbeat` (найди `function handle_tab_heartbeat`).
- `js/telegram_leads.js` целиком, `css/telegram_leads.css`, разметку вкладки в `index.html` (ищи
  `telegram-leads-tab`).

## Часть A. Вкладка видна, фронтенд честный

1. В `index.html` убери атрибут `hidden` у `#telegram-leads-tab`. Видимость панелей управляется только классом
   `active`, как у остальных вкладок.
2. `js/telegram_leads.js`:
   - **Опрос только когда вкладка открыта.** Опрашивай, только если у `#telegram-leads-tab` есть класс `active` и
     `document.visibilityState === 'visible'`. Интервал 15 секунд. При переходе на вкладку — сразу один опрос
     (смени `active` через `MutationObserver` на классе панели или подпишись на клики `.tab-btn[data-tab]`).
   - **Список целиком, без накопления.** Каждый опрос запрашивает первую страницу (20 штук) текущего статуса и
     **заменяет** список. Внизу кнопка «Показать ещё», если `total` больше показанного. Сейчас `currentOffset` растёт
     с каждым опросом: так нельзя.
   - **Пустое состояние вместо вечного спиннера.** Лидов нет → текст по ситуации из ответа сервера (часть B отдаёт
     `radar`): «Подключите аккаунт Telegram в Личном кабинете → Соцсети», «Добавьте чаты и включите радар» или
     «Пока лидов нет. Последняя проверка: 18:42, просмотрено 120 сообщений».
   - Обрезай текст **до** экранирования (`escapeHtml(text.slice(0, 300))`), а не после: иначе может разрезаться
     HTML-сущность.
   - В `logToRadarConsole` `message` вставляется через `innerHTML`. Переделай на `textContent` для текста; разметку
     нужна только для метки времени.
   - Ссылка на автора: `lead.profile_url`, которую собрал сервер. Без подстановки `https://t.me/${lead.user_id}`.
   - Если сервер прислал ссылку на сообщение (`message_url`), добавь в карточку «Открыть в Telegram ↗».
3. **Форма настроек радара** — новый блок в начале панели (над «Статистика»):
   - `<textarea>` «Чаты для наблюдения» (по одному в строке: `@username` или `https://t.me/username`);
   - чекбокс «Радар включён»;
   - кнопка «Сохранить» и строка статуса.
   Данные — из `GET leads_settings`, сохранение — `POST leads_settings_save` (часть B), с заголовком
   `X-Posta: 1`, как остальные POST.
   Стили — в `css/telegram_leads.css` через переменные темы из `css/app.css` (`--surface`, `--line-strong`,
   `--accent` и т.д.), без захардкоженных цветов.

**Проверка:** `node --check js/telegram_leads.js`. Опиши в отчёте, что пользователь должен увидеть при клике на
вкладку.

## Часть B. API только своих лидов + настройки

1. **Новая миграция** (следующий номер после последнего в `db_migrate`). Поле `user_id` в обеих таблицах теперь
   означает **владельца в приложении** (`app_user_id()`), а автор сообщения хранится отдельно:
   ```sql
   ALTER TABLE telegram_lead_queue ADD COLUMN chat_ref TEXT;          -- @username или id, как ввёл пользователь
   ALTER TABLE telegram_lead_queue ADD COLUMN chat_title TEXT;
   ALTER TABLE telegram_lead_queue ADD COLUMN chat_username TEXT;     -- для ссылки на сообщение; пусто у приватных
   ALTER TABLE telegram_lead_queue ADD COLUMN sender_id INTEGER;
   ALTER TABLE telegram_lead_queue ADD COLUMN sender_username TEXT;
   ALTER TABLE telegram_lead_queue ADD COLUMN sender_name TEXT;
   ALTER TABLE telegram_lead_queue ADD COLUMN message_date TEXT;
   CREATE UNIQUE INDEX IF NOT EXISTS telegram_lead_queue_unique ON telegram_lead_queue(user_id, chat_id, message_id);
   ALTER TABLE telegram_leads ADD COLUMN chat_title TEXT;
   ALTER TABLE telegram_leads ADD COLUMN sender_id INTEGER;
   ALTER TABLE telegram_leads ADD COLUMN sender_name TEXT;
   ALTER TABLE telegram_leads ADD COLUMN message_url TEXT;
   ALTER TABLE telegram_leads ADD COLUMN reason TEXT;                -- короткое «почему лид» от модели
   ```
   Таблицы сейчас пустые, переносить данные не нужно. Если миграции в проекте выполняются одним `exec` на блок —
   оформи так же.
2. `handle_telegram_leads`:
   - `WHERE user_id = ? AND lead_status = ?` с `app_user_id()`;
   - `status` — только из белого списка (`new`, `replied`, `interested`, `spam`, `ignored`), иначе `new`;
   - `limit` от 1 до 50;
   - отдаёт поля карточки: `id, message, chat_title, sender_name, telegram_username, profile_url, message_url,
     reason, lead_status, created_at`. `profile_url` собирай на сервере: `https://t.me/<username>`, если username
     есть, иначе `null` (на `tg://user?id=` не ссылайся: в браузере не работает);
   - плюс блок `radar`: `{connected: bool, enabled: bool, chats: int, last_run: ISO|null, last_seen: int,
     last_error: string|null}` для пустого состояния.
3. `handle_telegram_leads_stats` — те же счётчики, но `WHERE user_id = ?`. Очередь (`pending_in_queue`) — тоже
   только своя.
4. `handle_update_telegram_lead`: `UPDATE … WHERE id = ? AND user_id = ?`. Если строка не обновилась — 404
   «Лид не найден». Свою проверку `X-Posta` внутри функции убери: её уже делает `index.php` для всех POST.
   Ставь `replied_at`, когда статус `replied`.
5. Настройки радара храни в `kv` под ключом `'lead_radar:' . $uid`:
   ```php
   ['enabled' => bool, 'chats' => ['@name', ...], 'last_ids' => ['<chat_id>' => <последний message_id>],
    'last_run' => int|null, 'last_seen' => int, 'last_error' => ?string, 'resolved' => ['@name' => <chat_id>]]
   ```
   - `GET leads_settings` → `{enabled, chats, connected}`, где `connected` — `telegram_user_connection() !== null`;
   - `POST leads_settings_save` (`{enabled, chats: string}`): разбей по строкам; `https://t.me/name` и `t.me/name`
     приведи к `@name`; разрешены `@[A-Za-z0-9_]{4,32}` и числовые id вида `-100…`; не больше 30 чатов; дубли
     убери. Удалённым чатам почисти `last_ids`. Если включают радар без подключённого аккаунта — ошибка
     «Сначала подключите аккаунт Telegram в Личном кабинете».
   - Добавь оба действия в списки `GET`/`POST` в `index.php`.
6. Логику радара (настройки, проход, классификация) вынеси в новый файл `lib/lead_radar.php`. Обработчики в
   `handlers/content.php` остаются тонкими. Подключи его, добавив `'lead_radar'` в конец списка в `bootstrap.php:4`.
   `lib/ollama_classifier.php` подключай через `require_once` внутри `lib/lead_radar.php`.

**Проверка:** `php -l` по всем изменённым PHP-файлам. Потом миграция и изоляция на тестовом пользователе прямо
в базе, без браузера:
```bash
php -r 'require "bootstrap.php"; db(); echo db_one("PRAGMA user_version")["user_version"] ?? "?", PHP_EOL;'
```
Убедись, что версия выросла и новые колонки есть (`PRAGMA table_info(telegram_leads)`). Затем вставь двум
выдуманным `user_id` (`'test_a'`, `'test_b'`) по лиду, вызови SQL из `handle_telegram_leads` с `'test_a'` и проверь,
что пришёл только его лид. **Удали тестовые строки** после проверки.

## Часть C. Классификатор

Перепиши `lib/ollama_classifier.php` (имя файла оставь: на него ссылается README):
- константы `LEAD_MODEL = SMART_MODEL`, `LEAD_TIMEOUT = 120`, `LEAD_BATCH = 15`;
- `lead_classify(string $text): array` → `['lead' => 0|1, 'reason' => string]` или исключение;
- запрос **без размышлений** и с JSON-схемой. Используй `ollama_quick_json(LEAD_MODEL, $prompt, $format, 60,
  LEAD_TIMEOUT)` из `lib/ai.php`, где `$format` — JSON-схема:
  `{"type":"object","properties":{"lead":{"type":"integer","enum":[0,1]},"reason":{"type":"string"}},"required":["lead","reason"]}`.
  **Но сначала проверь `parser_session_alive()`:** если в CLI она может вернуть false, `ollama_stream` оборвёт
  запрос. Тогда напиши в этом файле свой короткий запрос: `stream: false`, `think: false`, `format`,
  `options: {temperature: 0, num_predict: 60}`, `keep_alive` как в `ollama_common_payload`, таймаут `LEAD_TIMEOUT`,
  разбор через `decode_model_json`;
- `null` от модели → `RuntimeException('Модель не ответила')`;
- **промпт целиком в `prompt`** (системного поля не используем), по-русски:
  ```
  Ты отбираешь потенциальных клиентов для сервиса Postä. Сервис сам находит свежий контент по теме (посты, мемы,
  видео из Telegram, VK, форумов) и публикует его в канал владельца по расписанию.

  Лид (1) — автор САМ ищет такое решение или жалуется на эту боль: ищет, где брать контент для канала, чем
  автоматизировать постинг, как вести канал без затрат времени, просит посоветовать сервис/бота для отложенных
  постов или подбора контента, ищет админа/контент-менеджера для наполнения канала.
  Не лид (0) — реклама своих услуг (SMM-специалист предлагает себя), продажа каналов, спам, флуд, вопросы не про
  контент и постинг, сообщения без запроса.

  Примеры:
  «Посоветуйте бота, чтобы посты сами выходили по расписанию» → 1
  «Где брать мемы для паблика каждый день? Устал искать вручную» → 1
  «Ищу человека, который будет наполнять мой телеграм-канал про авто» → 1
  «Веду ваши соцсети под ключ, пишите в лс» → 0
  «Продам канал 5к подписчиков» → 0
  «Кто-нибудь знает, когда обновят тарифы?» → 0

  Сообщение:
  """<текст, обрезанный до 1500 символов>"""

  Ответь JSON: {"lead": 0 или 1, "reason": "до 12 слов, почему"}
  ```
- `lead_prefilter(string $text): bool` — дешёвый отсев **до** модели, чтобы не грузить CPU:
  - длина после `trim` от 15 до 1500 символов;
  - есть хотя бы одно слово-маркер (регистр не важен, по корню): `контент`, `пост`, `автопост`, `отложен`,
    `расписан`, `канал`, `паблик`, `группу вк`, `админ`, `smm`, `смм`, `наполн`, `мем`, `рерайт`, `бот`. Список —
    константа `LEAD_KEYWORDS` с комментарием «маркеры для дешёвого отсева до модели»;
  - **и** есть признак запроса: `?`, или слова `ищу`, `посоветуйте`, `подскажите`, `нужен`, `нужна`, `кто знает`,
    `как `, `где `, `чем `, `помогите`.
- старые `classifyWithOllama`, `extractBinaryResult`, `getPendingLeadCount`, `getNewLeadsCount` удали, если на них
  больше никто не ссылается (`grep -rn`). `addToLeadQueue` замени новой функцией в `lib/lead_radar.php` (часть E).

**Проверка** (не больше 4 запросов к модели):
```bash
php -r 'require "bootstrap.php"; require "lib/ollama_classifier.php";
foreach (["Посоветуйте сервис, чтобы посты в канал выходили сами, а то не успеваю", "Продам канал про крипту 10к",
          "Где вы берёте свежие мемы для паблика?", "Всем привет, как дела"] as $t) {
  $p = lead_prefilter($t); echo ($p ? "→ модель: " . json_encode(lead_classify($t), JSON_UNESCAPED_UNICODE) : "отсеяно"), "  | $t\n"; }'
```
Ожидаемо: 1-е и 3-е → `lead: 1`, 2-е → 0 или отсеяно, 4-е отсеяно. Запиши время ответа модели. Если ответ
дольше 60 секунд, напиши об этом в отчёте, но модель не меняй.

## Часть D. Чтение чатов через аккаунт пользователя

В `tools/tg_user.py` добавь действие `read_chats` в стиле существующих (`find_channel` для разбора ссылок,
обработка ошибок через `describe`):

Запрос:
`{"action": "read_chats", "chats": [{"ref": "@name", "min_id": 123}], "limit": 100}`

Ответ:
```json
{"ok": true, "chats": [{"ref": "@name", "chat_id": -100123, "title": "…", "username": "name", "error": null,
   "messages": [{"id": 456, "date": 1729600000, "text": "…", "sender_id": 789, "sender_username": "vasya",
                 "sender_name": "Вася", "is_bot": false, "out": false}]}], "session": "…"}
```

- `client.iter_messages(entity, min_id=min_id, limit=limit)`. Если `min_id == 0` (первый запуск чата), бери только
  последние **30**, чтобы не утонуть в истории;
- пропускай служебные сообщения, сообщения без текста, свои (`out`), от ботов, а также каналы-витрины (у
  `broadcast`-каналов нет авторов: радар ищет людей в **группах и чатах**). Если чат — канал, верни для него
  `"error": "Это канал, а не чат: людей там не видно"` и пустой список;
- ошибка по одному чату (нет доступа, не существует, `FloodWait`) **не валит** весь запрос: пиши её в `error` этого
  чата и иди дальше. На `FloodWaitError` больше 60 секунд прекращай обход и верни то, что успел;
- имя отправителя: `first_name + last_name`, обрезка до 64 символов.

В `lib/telegram.php` рядом с `telegram_user_find_channels` добавь `telegram_user_read_chats(array $chats, $uid)`
— вызов через `telegram_user_call($uid, [...], 180)`.

**Проверка:** синтаксис — `python3 -I -c "import ast; ast.parse(open('tools/tg_user.py').read())"`. Реальный вызов
к Telegram **не делай**: аккаунт пользователя, решение за ним. Опиши в отчёте, как он может проверить сам (часть F).

## Часть E. Один проход радара

В `lib/lead_radar.php`:

`lead_radar_run(string $uid): array` — возвращает сводку `['seen' => n, 'queued' => n, 'classified' => n,
'leads' => n, 'errors' => [...]]`:
1. **Блокировка:** `flock` на `sys_get_temp_dir() . "/posta-radar-$uid.lock"` с `LOCK_EX | LOCK_NB`. Занято —
   сразу вернуть `['skipped' => 'уже идёт']`;
2. настройки из `kv`. Радар выключен, нет чатов или нет аккаунта — запиши `last_error` с понятным текстом и выйди;
3. `telegram_user_read_chats()` с `min_id` из `last_ids` (ключ — `chat_id`; для чата, который ещё ни разу не
   читали, — 0). Обнови `last_ids` максимальным `id`, `resolved`, ошибки чатов собери в `errors`;
4. каждое сообщение, прошедшее `lead_prefilter`, → `INSERT OR IGNORE INTO telegram_lead_queue (...)` с новыми
   колонками и `status = 'pending'`;
5. уборка очереди: `processing` старше 15 минут → снова `pending`; `failed` с `attempts < 3` → `pending`;
6. до `LEAD_BATCH` сообщений `pending` **этого пользователя**: `processing` → `lead_classify()` → `processed` с
   `ollama_result`. При `lead = 1` → `INSERT OR IGNORE INTO telegram_leads`, где `user_id` = владелец,
   `telegram_username`/`sender_name`/`sender_id` — автор, `message_url` =
   `https://t.me/<chat_username>/<message_id>` (только у публичных чатов, иначе `null`), `reason` от модели.
   Ошибка модели → `failed`, `attempts + 1`, `ollama_error`;
7. в `kv` запиши `last_run = time()`, `last_seen`, `last_error` (`null`, если всё хорошо);
8. блокировку отпусти в `finally`.

**Команда CLI** в `tools/cli.php`: `leads-scan <uid>` и `leads-scan --all` (все пользователи, у которых в kv
`enabled = true`; ключи ищи `SELECT key FROM kv WHERE key LIKE 'lead_radar:%'`). Печатает сводку по каждому.
Добавь её в справку CLI.

**Автозапуск, пока приложение открыто:** в `handle_tab_heartbeat` (`handlers/content.php:89`, сейчас одна строка с
`json_response`, а она завершает выполнение: запуск ставь **до** неё и оберни в `try/catch`, чтобы сбой радара не
ломал сердцебиение) — если у пользователя радар включён и
`last_run` старше 10 минут (или его нет), запусти в фоне
`/usr/bin/php tools/cli.php leads-scan <uid>` тем же способом, что в `handlers/content.php:312` (`escapeshellarg`
на каждый аргумент, вывод в `/dev/null`, `&`). Дубли отсекает `flock` из шага 1. Перед запуском запиши
`last_run = time()`: иначе каждое сердцебиение будет запускать новый процесс.

`tools/ollama_worker.php` перепиши в одну строку-обёртку: однократно вызывает тот же код, что `leads-scan --all`,
без бесконечного цикла. Обнови шапку-комментарий: «запуск из cron раз в 10 минут».

**Проверка:**
- `php -l lib/lead_radar.php tools/cli.php tools/ollama_worker.php handlers/*.php`;
- `php tools/cli.php leads-scan test_nobody` → понятное «Аккаунт Telegram не подключён» / «Радар выключен», без
  трейсбека;
- блокировка: запусти два `leads-scan test_nobody &` одновременно — второй должен ответить «уже идёт» либо оба
  завершиться без ошибок.

## Часть F. README

Перепиши `TELEGRAM_LEADS_README.md` коротко и по факту:
- как это работает (схема выше);
- что нужно от пользователя: подключённый аккаунт Telegram, чаты в радаре, галочка;
- как проверить вручную: `php tools/cli.php leads-scan <ваш uid>` (где взять uid — «Личный кабинет»);
- строка для cron, если нужен круглосуточный радар (пользователь добавляет сам):
  `*/10 * * * * /usr/bin/php /var/www/html/posta/tools/cli.php leads-scan --all >/dev/null 2>&1`;
- ограничения: модель на CPU, до 15 сообщений за проход, отсев по ключевым словам.

Убери из README упоминания `my-model:latest`, systemd и supervisor.

## Часть G. Итоговая проверка

- `php -l` по всем изменённым PHP, `node --check js/telegram_leads.js`, `python3 -I -c "import ast; ..."` по
  `tools/tg_user.py`;
- `grep -rn "classifyWithOllama\|my-model\|addToLeadQueue" --include=*.php --include=*.js --include=*.md .` — пусто
  (кроме этого промпта);
- `grep -n "hidden" index.html | grep telegram-leads` — пусто;
- тестовые строки в базе удалены: `SELECT COUNT(*) FROM telegram_leads WHERE user_id LIKE 'test_%'` = 0.

## Что сдать

Короткий отчёт пользователю по-русски:
- список изменённых и новых файлов (одна строка на файл: что в нём поменялось);
- результат проверки классификатора: 4 сообщения, ответы и время модели;
- что пользователь делает дальше: открыть «Радар лидов», вставить 2–3 открытых чата, где спрашивают про
  автопостинг, поставить галочку, подождать до 10 минут или запустить `php tools/cli.php leads-scan <uid>`;
- строка для cron (сам не добавляй);
- что не сделано и почему.
