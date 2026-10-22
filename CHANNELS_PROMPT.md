# Задача: несколько Telegram-каналов на пользователя

Ты работаешь в PHP-проекте `/var/www/html/posta` (Posta: ИИ подбирает контент по теме и публикует в Telegram).

Сейчас у пользователя ровно один канал: бот и канал лежат в `users.connections` под ключом `telegram`
(см. `telegram_connection()` в `lib/telegram.php`), и всё — публикация, отложенные посты, очередь — работает с ним.
Тарифы продают «до 5 каналов» («Сетка»), а подключить второй канал нельзя. Нужно:

1. **Таблица каналов:** у пользователя сколько угодно каналов (в пределах тарифа), у каждого свой бот или общий.
2. **Выбор канала при публикации:** в редакторе поста «Куда» — список каналов пользователя.
3. **Ничего не сломать:** существующий канал переезжает в таблицу сам, старые вызовы `telegram_connection()` работают
   как раньше (канал по умолчанию).

Свою очередь публикаций и расписание по слотам делает **следующая задача** (`SCHEDULE_PROMPT.md`) — здесь не начинай.
Тема поиска остаётся одна на пользователя — своя тема у канала будет позже, сейчас её не добавляй.

Делай части по порядку: A → B → C → D → E. Не начинай следующую, пока проверка предыдущей не прошла.

## Общие правила

- **Сначала прочитай целиком:** `lib/telegram.php` (`telegram_connection`, `telegram_bot_request`,
  `telegram_publish_prepared`, `telegram_message_url`, `telegram_user_schedule`, `telegram_user_cancel`,
  `telegram_user_channel_ref`, `telegram_record_native_post`, `telegram_settle_native_posts`), `handlers/account.php`
  (`account_telegram_public`, `handle_telegram_connect`, `handle_telegram_disconnect`, `require_bot`,
  `telegram_user_finish`, `handle_telegram_publish`, `handle_telegram_queue`, `handle_telegram_cancel`,
  `handle_telegram_user_qr_status`), `lib/db_access.php` (`connections_load/update`, `outbox_load/update`),
  `lib/db.php` (как устроены миграции), `lib/billing.php` (`billing_limits`, `BILLING_ENFORCE`), `lib/auth.php`
  (`user_merge` — его придётся дополнить), `tools/cli.php` (`login-bot-from-channel`), `index.html` (раздел
  `data-section="social"`), `js/account.js` (Telegram в «Соцсетях», `loadTelegramQueue`), `js/content.js`
  (`postItemToTelegram`, `refreshTelegramTarget`, `channelLabel`, `publishToTelegram`, панель публикации),
  `js/editor.js` (`openPostEditor`, `options.target`).
- **Не делай git commit.** Пользователь проверит изменения сам.
- **Не трогай файлы и функции, которые не названы в задаче.** Если без правки не названной функции не обойтись —
  сделай минимальную и упомяни её в отчёте.
- **Пиши в стиле проекта:** короткие функции, комментарии по-русски, без классов и новых библиотек. Ошибки для
  пользователя — `InvalidArgumentException` / `DomainException` / `json_error` с понятным русским текстом.
- **Не запускай парсер и модели. Не публикуй ничего в настоящие каналы** и не вызывай Telegram API, кроме `getMe` /
  `getChat` / `getChatMember` в ручной проверке (они ничего не меняют).
- **Не открывай сайт в безголовом браузере и не дёргай curl'ом без куки** — каждый заход без куки создаёт нового
  пользователя и может запустить фоновый парсер.
- **Перед первым запуском PHP-кода после правок сделай копию базы** (миграция применится сама):
  ```
  sqlite3 data/posta.sqlite ".backup data/posta.sqlite.bak-channels"
  ```

## Часть A. Таблица каналов

### A1. Миграция

В `lib/db.php` добавь миграцию со **следующим свободным номером** (наибольший ключ в `$migrations` + 1):

```sql
CREATE TABLE IF NOT EXISTS channels (
  id TEXT PRIMARY KEY,                       -- 'c' + 10 hex, bin2hex(random_bytes(5))
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  platform TEXT NOT NULL DEFAULT 'telegram',
  position INTEGER NOT NULL DEFAULT 0,       -- порядок в списках; 0 — канал по умолчанию
  token_enc TEXT NOT NULL,                   -- токен бота, secret_encrypt
  bot_id INTEGER NOT NULL,
  bot_username TEXT NOT NULL DEFAULT '',
  chat_id INTEGER NOT NULL,
  chat_title TEXT NOT NULL DEFAULT '',
  chat_username TEXT NOT NULL DEFAULT '',
  chat_type TEXT NOT NULL DEFAULT '',
  connected_at INTEGER NOT NULL,
  UNIQUE (user_id, chat_id)
);
CREATE INDEX IF NOT EXISTS channels_user ON channels(user_id, position);
```

### A2. Перенос существующих подключений

Перенос делает команда `tools/cli.php channels-migrate`, а не SQL-миграция: так его можно повторить и проверить, а
токены расшифровываются PHP-функциями. Команда:
- для каждого пользователя, у которого в `connections` есть `telegram` с `chat_id` и `token_enc`, а в `channels`
  ещё нет строки с этим `chat_id`, — вставляет строку `position = 0`;
- **не удаляет** `connections.telegram` (откат должен оставаться возможным; удалим ключ позже отдельной задачей);
- печатает `<8 символов id>: канал «название» (@имя) перенесён` / `уже есть`;
- с `--dry-run` только печатает.

И чтобы перенос не зависел от того, запустил ли кто-то команду: `channels_load($uid)` (A3), если у пользователя нет ни
одной строки в `channels`, но есть `connections.telegram`, — переносит его на лету тем же кодом (вынеси перенос
одного пользователя в функцию `channels_import_legacy($uid)`).

### A3. Доступ к каналам — `lib/db_access.php`, раздел `// ---- Каналы ----`

```php
/** Каналы пользователя по position. Токен расшифрован в поле 'token', token_enc убран. Битый токен — канал без 'token'. */
function channels_load($uid = null): array { ... }

/** Канал пользователя по id или null (чужой id — тоже null). */
function channel_get($channel_id, $uid = null): ?array { ... }

/** Добавить или обновить (по chat_id) канал; возвращает его id. Новый канал встаёт в конец списка. */
function channel_save(array $channel, $uid = null): string { ... }

function channel_delete($channel_id, $uid = null): bool { ... }

/** Сделать канал первым (каналом по умолчанию): его position = 0, остальные сдвигаются, порядок сохраняется. */
function channel_make_default($channel_id, $uid = null): void { ... }
```

### A4. Совместимость — `telegram_connection()` в `lib/telegram.php`

Новая сигнатура `telegram_connection($uid = null, $channel_id = null)`:
- `$channel_id` задан → этот канал пользователя или null;
- не задан → первый канал (`position` наименьший) или null.

Формат результата **тот же, что сейчас** (`token`, `bot_id`, `bot_username`, `chat_id`, `chat_title`, `chat_username`,
`chat_type`, `connected_at`) плюс `id` канала — вызывающий код от этого не ломается. Пройди все вызовы
(`grep -rn "telegram_connection(" lib handlers tools`) и убедись, что каждый либо правильно работает с каналом по
умолчанию, либо получает нужный `$channel_id` в частях C–D.

**Проверка части A:**
```
php -l lib/db.php && php -l lib/db_access.php && php -l lib/telegram.php && php -l tools/cli.php
php tools/cli.php channels-migrate --dry-run
php tools/cli.php channels-migrate
php -r 'require "bootstrap.php"; foreach (channels_load("b34cfe131b9ddea519d4cbd42c831fb5") as $c) echo $c["id"], " ", $c["chat_title"], " @", $c["chat_username"], " бот @", $c["bot_username"], " токен ", isset($c["token"]) ? "есть" : "НЕТ", "\n";
  $t = telegram_connection("b34cfe131b9ddea519d4cbd42c831fb5"); echo "по умолчанию: ", $t["chat_title"] ?? "нет", "\n";'
```
Ожидается: канал «жемчужина интернетов» (@internet_pearl), бот @s00perBot, токен есть; «по умолчанию» — он же.

## Часть B. Подключение и управление каналами

### B1. Сервер — `handlers/account.php`

- `handle_telegram_connect($in)`: вместо записи в `connections` — `channel_save(...)`. Проверки (формат токена,
  `getMe` / `getChat` / `getChatMember`, право публикации) оставь как есть.
  - **Токен можно не вводить**, если пользователь подключает ещё один канал тем же ботом: поле `bot` = id уже
    подключённого канала пользователя → берём токен оттуда. Бот должен быть администратором и в новом канале — эту
    проверку не пропускай.
  - **Лимит тарифа:** новый канал (такого `chat_id` у пользователя ещё нет) при `count(channels_load()) >=
    billing_limits()['channels']` — при `BILLING_ENFORCE` отказ `DomainException("На тарифе «…» можно подключить
    каналов: N. Больше — в «Тариф и баланс».")`; без `BILLING_ENFORCE` — подключить, но вернуть в ответе
    `'over_limit' => true` (интерфейс покажет предупреждение).
  - Ответ: `ok(['channels' => channels_public()])`.
- `handle_telegram_disconnect($in)`: параметр `channel` (id). Удаляет этот канал. Если у канала есть отложенные посты в
  «Отложенных» Telegram (`outbox`, `status = 'telegram'`, `channel` = этот id, см. часть D) —
  `DomainException('У канала есть отложенные посты: сначала отмените их в очереди.')`.
  Без параметра (старый интерфейс) — отказ «Не указан канал».
- Новое действие `telegram_channel_default` (POST, `channel`) → `channel_make_default` → `ok(['channels' => …])`.
- `channels_public()` — список для интерфейса: `id`, `title`, `username`, `bot_username`, `type`, `connected_at`,
  `default` (первый), **без токена**.
- `account_telegram_public()` и всё, что её использует (`handle_account_info`), — отдают канал по умолчанию как
  раньше **и** `channels => channels_public()`.
- Маршруты в `index.php`: добавь `telegram_channel_default` в `POST`.
- `require_bot()` — оставь (канал по умолчанию), но сообщение поправь на «Сначала подключите канал».
- Личный аккаунт (Telethon) один на пользователя и годится для любого канала, где он админ. В
  `telegram_user_finish` проверка прав сейчас идёт по каналу по умолчанию — оставь так, но в тексте предупреждения
  укажи, о каком канале речь (оно уже содержит название).

### B2. Интерфейс — «Личный кабинет» → «Соцсети» → Telegram

- Список каналов: название, @имя (ссылкой на `https://t.me/имя`, если имя есть), бот, пометка «по умолчанию»;
  у каждого кнопки «Сделать основным» (кроме первого) и «Отключить» (через `confirm`).
- Форма «Подключить канал»: если каналов нет — как сейчас (токен + канал). Если есть — выпадающий список «Бот»:
  «@бот (как у канала …)» для каждого уникального бота + «Новый бот (ввести токен)»; поле токена показывается только
  для нового бота.
- Под списком — строка «Каналов: N из M по тарифу» (M из `billing_info` → `plan.channels`, его уже отдаёт
  `handle_billing_info`). При `over_limit` — предупреждение «Больше каналов, чем в тарифе: когда включатся лимиты,
  публиковать можно будет только в первые M».
- Стили — существующими классами «Соцсетей»; новые — только с префиксом `channel-`.

**Проверка части B:**
```
php -l handlers/account.php && php -l index.php && node --check js/account.js
```

## Часть C. Публикация в выбранный канал

- **Цель поста.** Сейчас в коде цель — строка `tg:main`. Новый формат — `tg:<id канала>`; `tg:main` продолжает
  означать канал по умолчанию (старые черновики в браузере не должны сломаться).
- `js/content.js`: `refreshTelegramTarget` / `channelLabel` / панель публикации — вместо одного канала берут список
  каналов (из `account_info` → `channels` или отдельного запроса — выбери тот, что уже вызывается) и показывают выбор
  «Куда», если каналов больше одного. Выбор запоминается в `localStorage` (ключ `posta.channel`, чтение и запись в
  `try/catch`); сохранённый id, которого больше нет, — молча заменяется на канал по умолчанию.
- `js/editor.js`: чип «Куда» сейчас только показывает цель. Если каналов больше одного — по клику открывается список
  каналов (используй `bindMenu` из `js/core.js`), выбор меняет `options.target` и лимит длины текста. Список каналов
  передай в `openPostEditor` через `options.targets` (`[{value, label}]`); без `targets` чип ведёт себя как раньше.
- `publishToTelegram` отправляет `channel` = id из цели (`tg:main` → пусто).
- `handle_telegram_publish`: берёт `$in['channel']` (или `$_POST['channel']`) → `telegram_connection(null, $channel)`;
  пусто — канал по умолчанию; чужой или несуществующий id → `json_error('Канал не найден: обновите страницу.', 404)`.
  Отложенный пост через аккаунт (`telegram_user_schedule`) ставится **в выбранный** канал — функция уже принимает `$bot`,
  передай выбранный.
- **Лимит каналов при публикации** (только при `BILLING_ENFORCE`): публиковать можно в первые
  `billing_limits()['channels']` каналов по `position`; в остальные — `json_error("Канал за пределами тарифа …", 402)`.

## Часть D. Очередь и отложенные посты

- `telegram_record_native_post` сохраняет в записи `outbox` поле `channel` (id). Старые записи без `channel` относятся
  к каналу по умолчанию.
- `handle_telegram_cancel`: отменяет в «Отложенных» **того** канала, что записан в посте (`telegram_connection(null,
  $e['channel'] ?? null)`).
- `handle_telegram_queue`: в каждом посте очереди и журнала — `channel` и `channel_title`; в ответе `channels` вместо
  одного `channel` (старое поле `channel` оставь — канал по умолчанию — чтобы не сломать интерфейс, если где-то читается).
- `telegram_log_published`: в журнал опубликованных пишется `channel` и `channel_title` (и при публикации сразу, и при
  переносе отложенных в `telegram_settle_native_posts`).
- `js/account.js` `loadTelegramQueue`: у каждого поста — название канала (если каналов больше одного).
- **`user_merge`** в `lib/auth.php`: каналы `$from` переносятся к `$into` (`UPDATE channels SET user_id = …`), кроме
  тех, чей `chat_id` у `$into` уже есть (их удалить). Новые — в конец списка `$into`.
- **`tools/cli.php login-bot-from-channel`** берёт токен через `telegram_connection($uid)` — проверь, что работает.

## Часть E. Проверка без настоящего Telegram

Создай `tools/test_channels.php` (только CLI) и запусти **на копии проекта** (путь к базе — константа от папки):

```
T=$(mktemp -d) && cp -r bootstrap.php config.php config.local.php secret.key.php lib handlers tools "$T"/ \
  && mkdir "$T/data" "$T/users" "$T/media" && sqlite3 data/posta.sqlite ".backup $T/data/posta.sqlite" \
  && POSTA_MEDIA_CACHE_DIR="$T/media" php "$T/tools/test_channels.php"; rm -rf "$T"
```

Отдельная папка медиа обязательна: без `POSTA_MEDIA_CACHE_DIR` тест работал бы с настоящими файлами в
`/var/lib/posta/media` (переменная появляется в `MEDIA_RECOVERY_PROMPT.md`, часть A — он делается раньше).

Скрипт печатает `OK …` / `FAIL …` и в конце число провалов. Telegram не вызывать: каналы создавай через
`channel_save` с выдуманным токеном (`secret_encrypt('123:fake')`) и `chat_id`. Обработчики зовут `json_response` /
`exit` — проверки вынеси в функции и тестируй их (например, `channel_connect_denied($chat_id)` — текст отказа по
тарифу или null; `publish_channel_resolve($channel_id)` — канал или текст ошибки).

Сценарии (тестовый пользователь — новый, созданный в скрипте):
1. **Перенос:** у пользователя только `connections.telegram` → `channels_load` возвращает 1 канал, повторный вызов не
   создаёт второй; `connections.telegram` на месте.
2. **Совместимость:** `telegram_connection($uid)` — тот же `chat_id`, есть `token` и `id`.
3. **Второй канал:** `channel_save` с другим `chat_id` → 2 канала, первый остаётся по умолчанию;
   `channel_make_default(второй)` → `telegram_connection($uid)` теперь второй, порядок остальных сохранён.
4. **Повторное подключение того же chat_id** обновляет строку, а не добавляет.
5. **Чужой канал:** `channel_get(id_канала_другого_пользователя, $uid)` → null; `publish_channel_resolve` → ошибка.
6. **Лимит:** при тарифе на 1 канал и `BILLING_ENFORCE` (подмени через параметр функции или отдельную константу в
   тесте — не меняй `config.php`) второй канал → отказ; без enforce → разрешено.
7. **Удаление:** `channel_delete` → канала нет; канал с отложенным постом (`outbox` со `status = 'telegram'` и этим
   `channel`) удалить нельзя.
8. **Слияние:** у A каналы X и Y, у B каналы Y и Z → после `user_merge(A, B)` у B ровно X, Y, Z.
9. В конце удали тестовых пользователей.

Плюс `php -l` всех изменённых PHP и `node --check` изменённых JS.

## Отчёт пользователю

Перечисли изменённые файлы и шаги, которые нужны от него:
1. `php tools/cli.php channels-migrate` (если ещё не запускал — хотя перенос произойдёт и сам при первом заходе).
2. Открыть «Соцсети» → Telegram: канал «жемчужина интернетов» на месте; подключить второй канал тем же ботом
   (бот должен быть админом с правом публикации во втором канале).
3. В редакторе поста выбрать второй канал в «Куда» и опубликовать тестовый пост.

## Чего не делать

- Не удалять `connections.telegram` и не менять формат `connections`.
- Не отдавать токены ботов в интерфейс и не писать их в лог.
- Не делать свою очередь, расписание, слоты — это следующая задача.
- Не добавлять тему для канала.
- Не публиковать в настоящие каналы при проверке.
