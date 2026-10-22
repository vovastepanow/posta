# Задача: вход через Telegram и настоящие сессии

Ты работаешь в PHP-проекте `/var/www/html/posta` (Posta: ИИ подбирает контент по теме и публикует в Telegram).

Сейчас пользователь — это кука `ct_uid`, в которой лежит его id (32 hex). Почистил куки — потерял пул, историю, баланс
и тариф. Хуже того, этот же id показан в «Личном кабинете» (кнопка «Скопировать ID»): кто его узнал, тот может
поставить такую куку и войти в чужой профиль с деньгами. Нужно:

1. **Сессии:** в куке — случайный токен сессии, а не id пользователя. id перестаёт давать доступ.
2. **Вход через Telegram:** профиль привязывается к аккаунту Telegram, войти в него можно с любого браузера.
3. **Слияние:** если человек уже пользовался сайтом анонимно, а потом вошёл в существующий аккаунт, его анонимные
   оценки и деньги не теряются.

Делай части по порядку: 0 → A → B → C → D → E, затем «Дубль профиля владельца». Не начинай следующую, пока проверка предыдущей не прошла.

## Почему именно так (не меняй подход)

- **Не используй Telegram Login Widget.** Ему нужен домен, привязанный к боту через BotFather (`/setdomain`), и HTTPS.
  У сайта домена нет (в `config.local.php` заглушки «ВАШ_ДОМЕН»), сервер локальный.
- **Вход по ссылке на бота:** сайт создаёт одноразовый токен и открывает `https://t.me/<бот>?start=login_<токен>`.
  Бот присылает сообщение с кнопкой «Подтвердить вход». Нажатие кнопки подтверждает токен. Страница раз в 2 секунды
  спрашивает сервер «подтверждён ли токен» и, когда да, перезагружается уже войдя.
- **Без вебхука и без демона:** апдейты бота забирает `getUpdates`, а вызывает его сам опрос статуса со страницы
  (под неблокирующей блокировкой, чтобы два запроса не забирали апдейты одновременно). Пока никто не входит, бот молчит.
  Это нормально: ответ на «Подтвердить» придёт, как только страница спросит статус.
- **Кнопка подтверждения обязательна.** Без неё злоумышленник начнёт вход у себя, пришлёт жертве ссылку, жертва нажмёт
  «Старт» и тем самым войдёт злоумышленника в свой аккаунт. Кнопка с текстом «Если вы сами не нажимали „Войти“ на
  сайте — не подтверждайте» закрывает это.
- **В базе хранится только sha256 от токенов** (сессии и входа), сами токены — только в куке и в ссылке.

## Общие правила

- **Сначала прочитай целиком:** `lib/core.php` (`app_user_from_request`, `app_user_bind`, `user_touch`,
  `user_is_empty`, `users_gc`, `secret_encrypt`), `lib/db.php` (`db_migrate` — посмотри, как устроены миграции 1–4),
  `lib/db_access.php` (`history_load/update`, `pool_load/update`, `connections_load/update`, `kv_get/kv_update`),
  `lib/telegram.php` (`telegram_bot_request`, `telegram_error_text`), `lib/billing.php`, `handlers/account.php`
  (`handle_account_info`, `ACCOUNT_COOKIE_INFO`), `index.php` (списки `GET`/`POST`, порядок вызовов внизу),
  `js/account.js` (`renderProfile`, `busy`, `status`), `js/core.js` (`api`), `config.php` (как `$local` из
  `config.local.php` превращается в `define`), `tools/cli.php` (как устроены команды и справка).
- **Не делай git commit.** Пользователь проверит изменения сам.
- **Не трогай файлы и функции, которые не названы в задаче.**
- **Пиши в стиле проекта:** короткие функции, комментарии по-русски, без классов, без новых библиотек и Composer.
  Ошибки для пользователя — `InvalidArgumentException` / `RuntimeException` / `DomainException` с понятным русским
  текстом (их превращает в JSON обработчик в `index.php`).
- **Не запускай парсер и модели** (`cron_parser.php`, Ollama, `tools/ollama_worker.php`).
- **Не открывай сайт в безголовом браузере и не дёргай curl'ом без куки.** Каждый заход без куки создаёт нового
  пользователя и может запустить фоновый парсер. Проверяй логику через `php -r` / `php` со скриптом из части E.
- **Перед первым запуском любого PHP-кода после правок сделай копию базы** — миграция из части A применится сама при
  первом обращении к базе:
  ```
  sqlite3 data/posta.sqlite ".backup data/posta.sqlite.bak-login"
  ```

## Часть 0. Бот для входа

Новый бот **не нужен**. У владельца уже подключён бот канала **@s00perBot**: его токен лежит зашифрованным в
`users.connections` → `telegram.token_enc` (см. `telegram_connection()` в `lib/telegram.php`). `getUpdates` для него
нигде в проекте не вызывается, а публикации апдейты не нужны, так что он может служить и ботом входа.
Ключи приложения Telegram (`kv` → `telegram_app_keys`, api_id/api_hash) — для личного аккаунта через Telethon,
**для входа они не нужны, не трогай их**.

Бот входа — настройка сайта, хранится в `kv` под ключом `telegram_login_bot`:
`['token_enc' => secret_encrypt(токен), 'username' => имя без @, 'bot_id' => id]`.

1. Создай `lib/auth.php` (шапка-комментарий по-русски, как у других файлов `lib/`) и сразу подключи его в
   `bootstrap.php` в конец списка `$lib`. Первый раздел — `// ---- Бот входа ----`:
   ```php
   /** ['token' => …, 'username' => …] или null — бот входа не задан. */
   function telegram_login_bot() { ... }               // читает kv, расшифровывает secret_decrypt
   function telegram_login_configured() { return telegram_login_bot() !== null; }
   ```
2. В `tools/cli.php` команда `login-bot-from-channel <id пользователя>`: берёт `telegram_connection($uid)`, проверяет
   токен вызовом `getMe` (через `telegram_bot_request`), сохраняет в `kv` `telegram_login_bot`. Печатает
   `Бот входа: @username`. Ошибки — понятным текстом и `exit(1)`. Строку справки добавь в начало файла, как у других
   команд.
3. В `config.php` константы с комментариями: `SESSION_COOKIE = 'pm_sid'`, `LOGIN_REQUEST_TTL = 600` (10 минут на
   подтверждение), `SESSION_TOUCH_SECONDS = 600`.
4. В `handlers/content.php` в `KV_HIDDEN_KEYS` добавь `'telegram_login_bot'`, чтобы токен не показывался в
   «Базах данных».

Везде ниже «токен бота входа» — это `telegram_login_bot()['token']`, имя — `telegram_login_bot()['username']`.

**Проверка:**
```
php -l config.php && php -l tools/cli.php && php -l lib/auth.php
php tools/cli.php login-bot-from-channel b34cfe131b9ddea519d4cbd42c831fb5   # Бот входа: @s00perBot
```
Эта команда — единственное место, где можно обратиться к настоящему Telegram (`getMe` ничего не меняет).

## Часть A. Миграция 5

В `lib/db.php` добавь миграцию `5` (тем же способом, что 3 и 4):

```sql
ALTER TABLE users ADD COLUMN telegram_id INTEGER;
ALTER TABLE users ADD COLUMN tg_username TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN tg_name TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS users_telegram_id ON users(telegram_id) WHERE telegram_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,               -- sha256(токен из куки), hex
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  last_seen INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_user ON sessions(user_id);

CREATE TABLE IF NOT EXISTS login_requests (
  token_hash TEXT PRIMARY KEY,               -- sha256(токен из ссылки на бота), hex
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,   -- кто начал вход (сессия в этом браузере)
  created_at INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'asked', 'confirmed', 'used')),
  chat_id INTEGER,                           -- куда бот отправил кнопку
  telegram_id INTEGER,
  tg_username TEXT NOT NULL DEFAULT '',
  tg_name TEXT NOT NULL DEFAULT ''
);
```

**Проверка:**
```
php -l lib/db.php
php -r 'require "bootstrap.php"; echo db_value("pragma user_version"), "\n";'   # 5
sqlite3 data/posta.sqlite ".schema sessions" ".schema login_requests"
```

## Часть B. Сессии вместо id в куке

В `lib/auth.php` (создан и подключён в части 0) раздел `// ---- Сессии ----`.

### B1. Функции сессий

```php
/** Новая сессия: возвращает токен для куки (64 hex). В базе — только sha256 от него. */
function session_create($uid) { ... }

/** id пользователя по токену из куки или '' (нет такой сессии). Не чаще SESSION_TOUCH_SECONDS обновляет last_seen. */
function session_user($token) { ... }

function session_destroy($token) { ... }               // удалить одну сессию
function session_destroy_all($uid) { ... }             // «Выйти на всех устройствах»
```

- Токен: `bin2hex(random_bytes(32))`; хэш: `hash('sha256', $token)`. Проверка формата токена: `/^[a-f0-9]{64}$/`.
- Перед `INSERT INTO sessions` сделай `INSERT OR IGNORE INTO users (id, created_at)` (внешний ключ).
- Старые сессии чистит `users_gc` (см. B4): `last_seen` старше `USER_COOKIE_DAYS` дней.

### B2. Чистая функция выбора пользователя (её проверяет часть E)

```php
/**
 * Кто делает запрос. $sid — кука SESSION_COOKIE, $legacy — старая кука USER_COOKIE (id пользователя).
 * Возвращает ['uid' => id, 'sid' => токен для куки или null (кука не меняется), 'drop_legacy' => bool].
 */
function session_resolve($sid, $legacy) { ... }
```

Правила строго в таком порядке:
1. `$sid` — действующая сессия → `['uid' => её пользователь, 'sid' => null, 'drop_legacy' => $legacy !== '']`.
2. `$legacy` — корректный id (`is_valid_user_id`), такой пользователь **есть** в базе и у него `telegram_id IS NULL`
   → это анонимный пользователь со старой кукой: переводим на сессию.
   `['uid' => $legacy, 'sid' => session_create($legacy), 'drop_legacy' => true]`.
3. Иначе (нет куки, кука от удалённого пользователя или id **вошедшего** через Telegram пользователя) → новый
   анонимный пользователь: `$uid = bin2hex(random_bytes(16))`,
   `['uid' => $uid, 'sid' => session_create($uid), 'drop_legacy' => $legacy !== '']`.

Правило 3 для вошедших и есть закрытие дыры: id из профиля больше не пускает в аккаунт.

### B3. `app_user_from_request` в `lib/core.php`

Перепиши её тело (имя и место вызова в `index.php` не меняй):
- вызывает `session_resolve($_COOKIE[SESSION_COOKIE] ?? '', $_COOKIE[USER_COOKIE] ?? '')`;
- если `sid` не null — ставит куку `SESSION_COOKIE` (параметры как у нынешней `setcookie`: срок `USER_COOKIE_DAYS`,
  `secure` по HTTPS, `path` как сейчас, `httponly`, `samesite=Lax`); вынеси установку куки в функцию
  `session_cookie_set($token)` — она понадобится при входе и выходе;
- если `drop_legacy` — удаляет старую куку `USER_COOKIE` (`setcookie` с `expires => 1` и тем же `path`);
- возвращает `app_user_bind($uid)`.

Запрос, пришедший с куками, но параллельно с другим запросом без кук, может завести второго анонимного пользователя —
это существовало и раньше, отдельно не исправляй.

### B4. Чистка и описания

- `users_gc` в `lib/core.php`: в начале удаляй сессии с `last_seen < time() - USER_COOKIE_DAYS * DAY_SECONDS` и
  `login_requests` старше суток (кроме `dry_run`).
- `user_is_empty` в `lib/core.php`: пользователь с `telegram_id IS NOT NULL` — **не** пустой.
- `ACCOUNT_COOKIE_INFO` в `handlers/account.php`: замени описание `USER_COOKIE` на описание `SESSION_COOKIE`
  («Ваша сессия: случайный ключ, по нему сайт узнаёт ваш профиль. ID профиля в ней нет.») и добавь `USER_COOKIE` с
  пометкой «Старая кука: заменяется сессией при первом заходе и удаляется». В том же файле в `'http_only' => …`
  учитывай обе куки.

**Проверка части B:**
```
php -l lib/auth.php && php -l lib/core.php && php -l handlers/account.php
php -r 'require "bootstrap.php";
  $u = "b34cfe131b9ddea519d4cbd42c831fb5";
  $r = session_resolve("", $u); echo "старая кука → ", $r["uid"] === $u ? "тот же" : "ДРУГОЙ", ", sid ", strlen($r["sid"]), "\n";
  $r2 = session_resolve($r["sid"], ""); echo "по сессии → ", $r2["uid"] === $u ? "тот же" : "ДРУГОЙ", "\n";
  $r3 = session_resolve("", ""); echo "без кук → новый: ", $r3["uid"] !== $u ? "да" : "НЕТ", "\n";
  session_destroy($r["sid"]); session_destroy($r3["sid"]); db_exec("DELETE FROM users WHERE id = ?", [$r3["uid"]]);'
```
Ожидается: «тот же, sid 64», «тот же», «новый: да».

## Часть C. Вход через бота

В `lib/auth.php` раздел `// ---- Вход через Telegram (бот по ссылке, без домена и вебхука) ----`.

### C1. Начало входа

```php
/** ['url' => 'https://t.me/<бот>?start=login_<токен>', 'token' => <токен>]. Токен — bin2hex(random_bytes(16)). */
function login_start($uid) { ... }
```
- Не задан бот входа (`telegram_login_configured()` false) — `RuntimeException('Вход через Telegram не настроен на сервере.')`.
- Не больше 5 незавершённых запросов на пользователя за `LOGIN_REQUEST_TTL`, иначе `DomainException` «Слишком много
  попыток входа, подождите несколько минут».
- В `login_requests`: `token_hash`, `user_id`, `created_at`, `status = 'pending'`.
- Параметр `start` в Telegram: до 64 символов `[A-Za-z0-9_-]` — `login_` + 32 hex подходит.

### C2. Обработка одного апдейта — чистая функция (её проверяет часть E)

```php
/** Разбирает апдейт бота. Возвращает список вызовов API бота: [[метод, поля], …] — их выполнит login_poll_updates. */
function login_handle_update(array $update): array { ... }
```
- **`message`** с текстом `/start login_<32 hex>` в **личном** чате (`chat.type === 'private'`) от не-бота
  (`from.is_bot` false): найди запрос по хэшу токена в статусе `pending` и не старше `LOGIN_REQUEST_TTL`.
  - Нашёл → `status = 'asked'`, сохрани `chat_id`, `telegram_id = from.id`, `tg_username = from.username ?? ''`,
    `tg_name = trim(first_name . ' ' . last_name)`. Верни `sendMessage` в этот чат с текстом
    «Войти на сайт Posta как <tg_name>? Если вы сами не нажимали «Войти» на сайте — не подтверждайте.» и
    `reply_markup` = `json_encode(['inline_keyboard' => [[['text' => 'Подтвердить вход', 'callback_data' => 'login:<токен>']]]])`.
    Да, в `callback_data` уходит сам токен: он и так у этого человека в ссылке; до 64 байт — влезает.
  - Не нашёл или просрочен → `sendMessage` «Ссылка для входа устарела. Нажмите «Войти через Telegram» на сайте ещё раз.»
- **`message`** с другим текстом в личном чате → `sendMessage` «Я бот для входа на сайт Posta. Нажмите на сайте
  «Войти через Telegram».» Групповые чаты игнорируй.
- **`callback_query`** с `data` `login:<32 hex>`: найди запрос в статусе `asked`, не просроченный, и **проверь, что
  `callback_query.from.id` совпадает с сохранённым `telegram_id`** (кнопку не может нажать другой человек).
  - Совпало → `status = 'confirmed'`; верни `answerCallbackQuery` (`text` «Готово») и `editMessageText` в том же
    сообщении (`chat_id`, `message_id` из `callback_query.message`): «Вход подтверждён. Вернитесь на сайт — страница
    обновится сама.»
  - Иначе → `answerCallbackQuery` с `text` «Ссылка устарела, начните вход на сайте заново», `show_alert` true.
- Любой другой апдейт → `[]`.

### C3. Забор апдейтов

```php
/** Забирает апдейты бота входа и отвечает на них. Неблокирующая блокировка: второй одновременный вызов просто выходит. */
function login_poll_updates() { ... }
```
- Блокировка: `try_lock(USERS_DIR . '/.login-bot.lock')` (функция есть в `lib/core.php`); не получил — `return`.
  Освободи в `finally` (`flock(LOCK_UN)` + `fclose`).
- `telegram_bot_request(telegram_login_bot()['token'], 'getUpdates', ['offset' => kv_get('login_bot_offset', 0),
  'timeout' => 0, 'allowed_updates' => json_encode(['message', 'callback_query'])], [], 10)`.
  Посмотри в `telegram_bot_request`, что она возвращает при ошибке, и обработай так же, как соседний код.
- Для каждого апдейта: выполни вызовы из `login_handle_update` (ошибку отдельного вызова логируй `error_log` и иди
  дальше), затем `kv_update('login_bot_offset', fn() => $update['update_id'] + 1)` — **после каждого апдейта**, чтобы
  сбой на середине не обработал первые повторно.
- Если Telegram отвечает 409 (у бота стоит вебхук), допиши в текст ошибки: «у бота включён вебхук — выключите его:
  deleteWebhook».

### C4. Статус входа и завершение

```php
/** Для опроса страницы: 'pending' | 'done' | 'expired'. При 'done' пользователь уже переключён, кука сессии выставлена. */
function login_status($uid, $sid, $token) { ... }
```
- Сначала `login_poll_updates()`.
- Найди запрос по хэшу токена **и** `user_id = $uid` (чужой токен — как несуществующий → `'expired'`).
- `pending`/`asked` и не просрочен → `'pending'`; просрочен → `'expired'`.
- `confirmed` → в одной транзакции `status = 'used'` и `login_complete($uid, $sid, $row)`, вернуть `'done'`.

```php
/** Привязывает Telegram к профилю или переключает на уже существующий. Возвращает id пользователя после входа. */
function login_complete($uid, $sid, array $request) { ... }
```
Пусть `$owner` — пользователь с `telegram_id = $request['telegram_id']`.
1. **`$owner` нет** → привязать к текущему: `UPDATE users SET telegram_id, tg_username, tg_name WHERE id = $uid`.
   Сессию **пересоздать** (`session_destroy($sid)`, `session_cookie_set(session_create($uid))`) — защита от подмены
   сессии. Вернуть `$uid`.
2. **`$owner === $uid`** → обновить `tg_username`/`tg_name`, пересоздать сессию, вернуть `$uid`.
3. **`$owner` другой** → это вход в существующий аккаунт.
   - Если текущий пользователь не пустой (`!user_is_empty($uid)`) → `user_merge($uid, $owner)`;
     если пустой — просто удалить его (`DELETE FROM users WHERE id = ?`).
   - `session_destroy($sid)`, `session_cookie_set(session_create($owner))`, `app_user_bind($owner)`.
   - Обнови у `$owner` `tg_username`/`tg_name`. Вернуть `$owner`.

Перед удалением или слиянием текущего пользователя останови его парсер: `stop_content_parser()` (работает для
привязанного `app_user_id()`, вызывай, пока привязан старый).

### C5. Слияние профилей

```php
/** Переносит в $into всё ценное из $from и удаляет $from. Всё в одной транзакции db_tx. */
function user_merge($from, $into) { ... }
```
- **История:** для каждой коллекции из `HISTORY_COLLECTIONS` добавь в конец записи `$from`, которых нет в `$into`
  (сравнение по `content_image_key`). Через `history_load` / `history_update($fn, $into)`.
- **Пул:** остаётся пул `$into`; если он пустой — взять пул `$from` (`pool_update`).
- **Настройки** (`users.settings`): остаются у `$into`.
- **Соцсети** (`connections_load/update`): ключи `$into` не трогать, недостающие ключи взять из `$from`.
- **Деньги и учёт:** `UPDATE ledger SET user_id = $into WHERE user_id = $from`; то же для `post_usage`, `outbox`,
  `published`. Записи `ledger.related_user = $from` поменяй на `$into`.
- **Тариф:** если у `$from` подписка с более поздним `until`, чем у `$into` (или у `$into` её нет) — перенеси её
  (`INSERT OR REPLACE` с `user_id = $into`), иначе оставь подписку `$into`.
- **Приглашения:** `UPDATE users SET referred_by = $into WHERE referred_by = $from AND id <> $into`. У самого `$into`
  `referred_by` заполни из `$from`, только если у `$into` пусто и это не `$into` сам себе.
  `ref_code` у `$from` пропадёт вместе с ним — это нормально.
- **Убрать:** `parser_runtime` и `sessions` у `$from` удалятся каскадом при `DELETE FROM users WHERE id = $from`.
  Папку `users/<from>` удали через `remove_dir`, как в `users_gc`.

**Проверка части C:** `php -l lib/auth.php`. Поведение проверяет часть E.

## Часть D. API и интерфейс

### D1. Маршруты (`index.php`) и обработчики (`handlers/account.php`, раздел `// ---- Вход через Telegram ----`)

| Действие | Метод | Что делает |
|---|---|---|
| `auth_status` | GET | `['configured' => bool, 'bot' => telegram_login_bot()['username'] ?? '', 'logged_in' => bool, 'tg_username', 'tg_name', 'sessions' => число сессий пользователя]` |
| `login_start` | POST | `login_start(app_user_id())` → `ok(['url' => …, 'token' => …])` |
| `login_status` | GET | параметр `token` (проверь формат 32 hex) → `json_response(['status' => login_status(app_user_id(), $_COOKIE[SESSION_COOKIE] ?? '', $token)])` |
| `logout` | POST | `session_destroy(текущая)`, новый анонимный пользователь (`bin2hex(random_bytes(16))`), `session_cookie_set(session_create(...))` → `ok()` |
| `logout_all` | POST | `session_destroy_all(app_user_id())`, дальше как `logout` |

- `logout` и `logout_all` для **не вошедшего** пользователя → `DomainException` «Вы не входили через Telegram: выход
  сотрёт ваш анонимный профиль» (иначе анонимный человек потеряет данные одним кликом).
- `login_status` вызывается раз в 2 с: в `index.php` добавь его рядом с `proposals_progress` — в ранний выход до
  `ensure_default_project_data()`, но **после** `app_user_from_request()`.
- В ответ `handle_account_info` добавь `'auth' => ['logged_in' => …, 'tg_username' => …, 'tg_name' => …]`.

### D2. Интерфейс (`js/account.js`, `index.html`, `css/app.css`)

В карточке профиля (`renderProfile`) под заголовком — блок входа:
- **Бот не настроен** (`configured` false): серый текст «Вход через Telegram появится, когда администратор подключит
  бота. Пока профиль держится на куке этого браузера — не чистите куки.»
- **Не вошёл:** текст «Профиль привязан только к этому браузеру. Войдите через Telegram, чтобы не потерять оценки и
  баланс и открывать профиль с других устройств.» и кнопка `primary-button` «Войти через Telegram».
  - Нажатие: `api('login_start', { body: null })` → `window.open(url, '_blank', 'noopener')`; если окно не открылось
    (вернулся `null`) — покажи ссылку текстом «Откройте бота: <ссылка>».
  - Пока ждём: текст «Откройте бота, нажмите «Старт», затем «Подтвердить вход». Ждём подтверждения…» и кнопка
    «Отмена». Опрос `api('login_status', { params: { token } })` раз в 2 с, не дольше 10 минут.
  - `done` → `location.reload()`. `expired` → «Время на вход вышло, попробуйте ещё раз» и кнопка снова активна.
    Ошибка сети — не прерывать опрос, просто следующий тик.
- **Вошёл:** «Вы вошли через Telegram: <tg_name> (@tg_username)», кнопки `account-secondary` «Выйти» и «Выйти на
  всех устройствах» (вторую показывай, если `sessions > 1`). Обе — через `confirm` и `location.reload()` после успеха.
- Кнопку «Скопировать ID» и показ ID **оставь**: теперь ID не даёт доступа, а нужен для пополнения баланса
  администратором (`tools/cli.php balance-add`).
- Стили — только существующими классами (`account-muted`, `primary-button`, `account-secondary`); новые классы —
  только если без них никак, с префиксом `auth-`.

**Проверка части D:**
```
php -l index.php && php -l handlers/account.php
node --check js/account.js
grep -n "auth_status\|login_start\|login_status\|logout" index.php
```

## Часть E. Проверка без настоящего Telegram

Создай `tools/test_login.php` (запуск только из CLI, как `tools/cli.php`). Он должен работать **на копии базы**, а путь
к базе — константа `DB_FILE` от папки проекта. Поэтому проект копируется во временную папку и скрипт запускается там:

```
T=$(mktemp -d) && cp -r bootstrap.php config.php config.local.php secret.key.php lib handlers tools "$T"/ \
  && mkdir "$T/data" "$T/users" "$T/media" && sqlite3 data/posta.sqlite ".backup $T/data/posta.sqlite" \
  && POSTA_MEDIA_CACHE_DIR="$T/media" php "$T/tools/test_login.php"; rm -rf "$T"
```

Скрипт печатает `OK …` / `FAIL …` по каждому пункту и в конце число провалов. Функции
`telegram_bot_request` в тесте не вызываются: проверяй `login_handle_update` (она возвращает вызовы, а не делает их)
и `login_complete` / `user_merge` напрямую. Куки в CLI не ставятся — `session_cookie_set` при `PHP_SAPI === 'cli'`
должна просто ничего не делать (добавь это условие).

Сценарии:
1. **Новый вход.** Анонимный A, `login_start(A)` → апдейт `/start login_<т>` от tg 111 → в ответе `sendMessage` с
   кнопкой, статус `asked` → `callback_query` от tg 111 → `confirmed` → `login_status(A, …)` = `done`, у A
   `telegram_id = 111`.
2. **Чужая кнопка.** Как 1, но `callback_query` от tg 222 → статус остаётся `asked`, ответ — `show_alert`.
3. **Просрочка.** Запрос с `created_at` = сейчас − 700 → `/start` отвечает «Ссылка для входа устарела».
4. **Чужой токен.** `login_status(B, …, токен_от_A)` = `expired`.
5. **Вход в существующий с пустого.** Пустой анонимный C входит как tg 111 → вернулся A, C удалён из `users`.
6. **Слияние.** Анонимный D с 2 оценками (одна совпадает по картинке с оценкой A) и записью в `ledger` на 100 ₽ входит
   как tg 111 → у A прибавилась ровно 1 оценка, баланс A вырос на 100 ₽, D удалён, записей `ledger` с `user_id = D` нет.
7. **Старая кука вошедшего.** `session_resolve('', A)` → новый пользователь, не A.
8. **Сообщение в группе** (`chat.type = 'group'`) → `[]`.
9. **`users-merge`** (см. ниже) на копии: `a6adaaa5…` в `b34cfe13…` → оценок у `b34cfe13` стало больше ровно на число
   неповторяющихся, `a6adaaa5` удалён, подключения `b34cfe13` не изменились.

Плюс: `php -l` всех изменённых файлов, `node --check js/account.js`.

Заверши работу, только когда все 9 сценариев `OK`. В отчёте пользователю перечисли изменённые файлы и напомни шаги,
которые нужны от него:
1. Выполнить `php tools/cli.php login-bot-from-channel b34cfe131b9ddea519d4cbd42c831fb5` (если ещё не сделано).
2. Открыть сайт в своём браузере → «Личный кабинет» → «Войти через Telegram» и пройти вход вручную.
3. После входа свести старый дубль профиля: `php tools/cli.php users-merge a6adaaa59a101ad6941434cb27989586
   b34cfe131b9ddea519d4cbd42c831fb5` (сначала с `--dry-run`).

## Дубль профиля владельца

В базе два профиля одного человека: `b34cfe13…` (основной, по нему сейчас ходит браузер) и `a6adaaa5…` (старый,
поднят из JSON-архива; тот же бот @s00perBot и аккаунт @citizen_erased). Добавь в `tools/cli.php` команду
`users-merge <из> <в> [--dry-run]`: проверяет, что оба id существуют и различаются; с `--dry-run` печатает, сколько
оценок, записей `ledger`, `published`, `outbox` будет перенесено (без изменений); без флага вызывает
`user_merge($from, $into)` и печатает итог. Строку справки добавь в начало файла.
**Саму команду на настоящей базе не запускай** — это сделает пользователь (шаг 3 в отчёте).

## Чего не делать

- Не использовать Login Widget, вебхуки, OAuth, сторонние библиотеки.
- Не класть токен бота в `config.local.php` или другие файлы открытым текстом — только `kv` через `secret_encrypt`.
- Не трогать подключение канала (`connections.telegram`) и ключи приложения (`telegram_app_keys`).
- Не хранить токены сессий и входа в открытом виде.
- Не передавать данные Telegram (id, имя) через параметры адреса — только через базу по подтверждённому запросу.
- Не менять `app_user_bind` / `app_user_id` и то, как CLI и парсер получают пользователя (аргумент командной строки).
- Не удалять старую куку `USER_COOKIE` из констант: она нужна для перевода старых пользователей на сессии.
- Не трогать тарифы, баланс и реферальную логику, кроме переноса строк в `user_merge`.
