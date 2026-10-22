# Задача: перевести хранилище Posta с JSON-файлов на SQLite

Ты работаешь в репозитории `/var/www/html/posta` (PHP 8.5, без фреймворка, без Composer). Сейчас все данные
лежат в JSON-файлах, а целостность держится на `flock` (`lib/core.php`: `load_data`, `save_data`, `with_file_lock`,
`update_data`). Нужно перенести данные в одну базу SQLite через PDO, не меняя поведение приложения и JSON API.

Работай строго по этапам ниже. После каждого этапа запускай проверки из раздела «Проверка» и делай коммит.
Если проверка не проходит, исправь ошибку до перехода к следующему этапу. Не пропускай этапы и не объединяй их.

---

## 0. Прочитай перед началом

Прочитай целиком: `README.md`, `config.php`, `bootstrap.php`, `index.php`, `lib/core.php`, `lib/runtime.php`.
Затем найди все места работы с хранилищем:

```sh
grep -rn -E "(load_data|save_data|update_data|with_file_lock)\(|_file\(|SOURCES_FILE|ANALYSIS_CACHE_FILE|SCREENED_CACHE_FILE|TELEGRAM_LIMITS_FILE|TELEGRAM_APP_KEYS_FILE|last-seen|proposals-checked|threads-checked|USERS_DIR \. \"/\\\$uid" lib handlers tools/cli.php index.php cron_parser.php
```

Каждое найденное место в итоге должно работать через новый слой БД, кроме исключений из раздела «Что НЕ переносить».

### Правила стиля (обязательно)
- Пиши как в окружающем коде: короткие функции в snake_case, однострочные `fn`, комментарии на русском, их столько же, сколько в соседнем коде.
  Тексты ошибок для пользователя пиши по-русски.
- Не добавляй Composer, ORM, классы-репозитории и новые зависимости. Достаточно PDO и функций.
- Не меняй формат ответов API (`index.php`, `handlers/*`) и JS-фронтенд (`js/`). Фронтенд не должен заметить переход.
- Не трогай `tools/tg-lib/`, `tools/tg_user.py`, промпты в `lib/ai.php` и пороги в `config.php`, если задача этого не требует.
- Сообщения коммитов пиши по-русски, как в `git log`.

---

## 1. Предусловие: драйвер SQLite

Сейчас в PHP **нет** драйвера SQLite (`PDO::getAvailableDrivers()` возвращает пустой массив). Проверь:

```sh
php -r 'var_dump(PDO::getAvailableDrivers());'
```

Если в списке нет `sqlite`, **остановись и попроси пользователя** выполнить команды (сам `sudo` не запускай):

```sh
sudo apt install php8.5-sqlite3 sqlite3
sudo systemctl restart apache2
```

Затем допиши `pdo_sqlite` в раздел «Требования» в `README.md`.

---

## 2. Слой БД: новый файл `lib/db.php`

Добавь `'db'` в список в `bootstrap.php` **первым** после `core` (или перед ним, если `core` будет вызывать `db()`).

В `config.php`, раздел «Данные», добавь:
```php
const DB_FILE = ROOT . '/data/posta.sqlite';          // вся база: пользователи, пулы, история, каталог, кэши
const DB_BUSY_TIMEOUT_MS = 15000;
```

Папку `data/` создай с `.htaccess` (`Require all denied`) и пустым `index.html`. Права: `2775`, группа `www-data`.

Функции в `lib/db.php` (имена и семантика обязательны):

```php
function db(): PDO
```
- Одно соединение на процесс (`static $pdo`).
- `PDO::ATTR_ERRMODE => PDO::ERRMODE_EXCEPTION`, `ATTR_DEFAULT_FETCH_MODE => FETCH_ASSOC`.
- Сразу после подключения: `PRAGMA journal_mode=WAL`, `PRAGMA busy_timeout=<DB_BUSY_TIMEOUT_MS>`,
  `PRAGMA foreign_keys=ON`, `PRAGMA synchronous=NORMAL`.
- Перед созданием файла `umask(0002)`, чтобы файлы `-wal` и `-shm` тоже были доступны группе: сайт работает
  как `www-data`, а `tools/cli.php` запускает пользователь `v0v4`.
- Вызывает `db_migrate($pdo)`.

```php
function db_migrate(PDO $pdo): void
```
- Версия схемы хранится в `PRAGMA user_version`. Миграции задаются массивом `[версия => SQL]` и применяются по порядку
  внутри `BEGIN IMMEDIATE`. Повторный вызов ничего не делает.

```php
function db_tx(callable $fn)
```
- `BEGIN IMMEDIATE` → `$fn()` → `COMMIT`, при исключении `ROLLBACK` и проброс исключения. Возвращает результат `$fn`.
- Поддерживает вложенность: если транзакция уже открыта (`$pdo->inTransaction()`), просто вызывает `$fn()`.
- **Заменяет `with_file_lock`/`update_data` для всех данных в БД.** Любая операция «прочитать → изменить → записать»
  выполняется внутри одного `db_tx`.

```php
function db_all(string $sql, array $params = []): array
function db_one(string $sql, array $params = []): ?array
function db_value(string $sql, array $params = [])
function db_exec(string $sql, array $params = []): int      // количество затронутых строк
function db_json($value): string                             // json_encode с JSON_UNESCAPED_UNICODE | JSON_INVALID_UTF8_SUBSTITUTE
function db_unjson(?string $json, $default = [])            // json_decode(..., true), при ошибке $default
```

Используй только подготовленные запросы с параметрами. Не подставляй значения в SQL строками.

---

## 3. Схема (миграция версии 1)

Принцип: запись хранится целиком в колонке `data` (JSON), а поля, по которым идёт поиск, сортировка или
удаление, вынесены в отдельные колонки с индексами. Так PHP-код продолжает работать с теми же массивами, что и сейчас.

```sql
CREATE TABLE users (
  id TEXT PRIMARY KEY,                 -- 32 hex, как кука ct_uid
  created_at INTEGER NOT NULL,
  last_seen INTEGER NOT NULL DEFAULT 0,          -- было: файл users/<id>/last-seen (mtime)
  proposals_checked_at INTEGER NOT NULL DEFAULT 0, -- было: users/<id>/proposals-checked
  threads_checked_at INTEGER NOT NULL DEFAULT 0,   -- было: users/<id>/threads-checked
  settings TEXT NOT NULL DEFAULT '{}',           -- было: settings.json
  connections TEXT NOT NULL DEFAULT '{}'         -- было: connections.json (токены уже зашифрованы, хранить как есть)
);

CREATE TABLE pool_items (               -- было: users/<id>/pool.json (список)
  id INTEGER PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  position INTEGER NOT NULL,            -- порядок, как в массиве
  image_key TEXT NOT NULL,              -- content_image_key($item)
  data TEXT NOT NULL
);
CREATE INDEX pool_items_user ON pool_items(user_id, position);
CREATE INDEX pool_items_key ON pool_items(image_key);

CREATE TABLE history_items (            -- было: users/<id>/history.json {liked|disliked|undecided|complaints: [...]}
  id INTEGER PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  collection TEXT NOT NULL,             -- одно из HISTORY_COLLECTIONS
  position INTEGER NOT NULL,
  image_key TEXT NOT NULL,
  proposal_hidden INTEGER NOT NULL DEFAULT 0,
  data TEXT NOT NULL
);
CREATE INDEX history_items_user ON history_items(user_id, collection, position);
CREATE INDEX history_items_key ON history_items(image_key);

CREATE TABLE outbox (                   -- было: outbox.json
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  publish_at INTEGER NOT NULL,
  status TEXT NOT NULL,
  data TEXT NOT NULL
);
CREATE INDEX outbox_user ON outbox(user_id, publish_at);

CREATE TABLE published (                -- было: published.json (последние TELEGRAM_PUBLISHED_LOG_LIMIT)
  id INTEGER PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  at INTEGER NOT NULL,
  data TEXT NOT NULL
);
CREATE INDEX published_user ON published(user_id, id DESC);

CREATE TABLE parser_runtime (           -- было: parser-v3.json и parser-v3.session
  user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  pid INTEGER NOT NULL DEFAULT 0,
  started_at INTEGER NOT NULL DEFAULT 0,
  tab TEXT NOT NULL DEFAULT '',
  tab_seen INTEGER NOT NULL DEFAULT 0,
  tab_opened_at INTEGER NOT NULL DEFAULT 0
);

-- Общий каталог (было: sources_db.json)
CREATE TABLE catalog_sources (          -- 2ch_boards (строки), vk_publics, telegram_channels
  kind TEXT NOT NULL,                   -- '2ch_boards' | 'vk_publics' | 'telegram_channels'
  source_id TEXT NOT NULL,              -- для досок — само имя, для остальных — поле id
  position INTEGER NOT NULL,
  data TEXT NOT NULL,                   -- для досок JSON-строка ("b"), для остальных объект
  PRIMARY KEY (kind, source_id)
);
CREATE TABLE source_votes (id INTEGER PRIMARY KEY, source_id TEXT NOT NULL, position INTEGER NOT NULL, data TEXT NOT NULL);
CREATE TABLE source_suggestions (id INTEGER PRIMARY KEY, source_id TEXT NOT NULL, position INTEGER NOT NULL, data TEXT NOT NULL);
CREATE TABLE topics (id TEXT PRIMARY KEY, position INTEGER NOT NULL, data TEXT NOT NULL);
CREATE TABLE kv (key TEXT PRIMARY KEY, value TEXT NOT NULL); -- discovery, dvach_topic_boards, telegram_limits, telegram_app, dvach_boards, gc_at и т. п.

-- Общие кэши
CREATE TABLE analysis_cache (key TEXT PRIMARY KEY, analysis TEXT NOT NULL, at INTEGER NOT NULL);
CREATE INDEX analysis_cache_at ON analysis_cache(at);
CREATE TABLE screened_cache (url TEXT PRIMARY KEY, topic TEXT NOT NULL, source TEXT NOT NULL DEFAULT '', score INTEGER NOT NULL, at INTEGER NOT NULL);
CREATE INDEX screened_cache_at ON screened_cache(at);
CREATE INDEX screened_cache_topic ON screened_cache(topic);
```

Перед тем как писать схему, проверь по коду реальные поля записей (`content_image_key`, `analysis_cache_key`,
формат `screened_cache.json`, ключи `connections.json`, поля `outbox`). Если поле называется иначе, исправь схему,
а не код. Если в одном JSON-файле лежат данные разных форматов, сохрани их в `data` без потерь.

---

## 4. Функции доступа (замена файлов)

Функции `*_file()` (`pool_file`, `history_file`, `settings_file`, `connections_file`, `published_file`,
`outbox_file`, `parser_runtime_file`) после переноса **удали** вместе с константами `SOURCES_FILE`,
`ANALYSIS_CACHE_FILE`, `SCREENED_CACHE_FILE`, `TELEGRAM_LIMITS_FILE`, `TELEGRAM_APP_KEYS_FILE`.
Вместо них заведи функции с той же семантикой «прочитать всё / изменить под транзакцией»:

| Было | Стало |
|---|---|
| `load_data(pool_file($uid), [])` | `pool_load($uid = null): array` (список по `position`) |
| `update_data(pool_file($uid), [], $fn)` | `pool_update(callable $fn, $uid = null): array`: внутри `db_tx` загрузить, `$fn($pool)`, если вернул `null`, ничего не делать, иначе переписать строки пользователя (DELETE + INSERT с новыми `position`) |
| `load_history()` / `load_data(history_file(...))` | `history_load($uid = null): array` в прежнем формате `[collection => [...]]` |
| `update_data(history_file(), empty_history(), $fn)` | `history_update(callable $fn, $uid = null): array` |
| — | `history_append($collection, array $record, $uid = null)`: один INSERT, для голосования (`handlers/content.php`, `apply_vote`) |
| settings.json | `load_settings()` / `save_settings()` читают и пишут `users.settings`, сигнатуры не менять |
| connections.json | `connections_load($uid = null)`, `connections_update(callable $fn, $uid = null)` |
| outbox.json / published.json | `outbox_load`, `outbox_update`, `published_load($uid, $limit)`, `published_add($uid, $entry)` (с обрезкой до `TELEGRAM_PUBLISHED_LOG_LIMIT`) |
| `load_source_db()` / `update_source_db($fn)` | сигнатуры те же. Внутри собрать массив из `catalog_sources`, `source_votes`, `source_suggestions`, `topics`, `kv`, прогнать через `normalize_source_db`, а при записи в `db_tx` переписать таблицы каталога |
| `load_data(ANALYSIS_CACHE_FILE)[$key]` | `analysis_cache_get($key)`: одна строка с проверкой срока `ANALYSIS_CACHE_DAYS` |
| запись в analysis_cache | `analysis_cache_put($key, $analysis)`: `INSERT OR REPLACE`, затем удалить просроченные и лишние сверх `ANALYSIS_CACHE_LIMIT` (самые старые по `at`) |
| screened_cache | `screened_cache_load()` (свежие), `screened_cache_put(array $entries)`, удаление по теме для `lib/topics.php:348`; лимиты `SCREENED_CACHE_LIMIT/DAYS` как сейчас |
| TELEGRAM_LIMITS_FILE / TELEGRAM_APP_KEYS_FILE / `.dvach-boards.json` | `kv_get($key, $default)`, `kv_update($key, callable $fn, $default)` |
| parser-v3.json, parser-v3.session | `parser_state()`, `tab_session_state()`, `tab_session_*` работают через `parser_runtime`; `update_data(tab_session_file(), ...)` заменить на `db_tx` |
| `user_ids()` | `SELECT id FROM users` |
| `user_touch()` / last-seen / `.gc` | `users.last_seen`, время последней чистки хранится в `kv` (`gc_at`). Удаление брошенного профиля: `DELETE FROM users` (каскад) **плюс** `remove_dir(user_dir($uid))` для журнала |
| proposals-checked / threads-checked (mtime) | `users.proposals_checked_at` / `users.threads_checked_at` |
| `all_users_pool_items()` | два запроса по всем пользователям (`pool_items` и `history_items` с `collection IN (PROPOSAL_COLLECTIONS) AND proposal_hidden = 0`), без цикла по файлам |
| `ensure_default_project_data()` | `INSERT OR IGNORE INTO users` с настройками по умолчанию, каталог по умолчанию при пустых таблицах |

Пользователь создаётся в `users` при первом обращении (`app_user_from_request` → `ensure_default_project_data`).
Каждая функция с `$uid = null` берёт `app_user_id()`.

Обязательно сохрани поведение `update_data`: если мутатор вернул `null`, данные не меняются, а функция возвращает
текущее состояние.

### Важные места, где легко ошибиться
- `handlers/content.php:102-104`: голосование меняет пул и **внутри** мутатора пула дописывает историю. Обе записи
  должны идти в одном `db_tx` (вложенный `db_tx` уже это обеспечивает).
- `lib/parser.php:204`: добавление в пул проверяет, что записи нет в пуле и в истории. Проверка и вставка должны
  быть в одной транзакции.
- `media_cache_release_removed($before, $after)` и сборка мусора кэша медиа опираются на `all_users_pool_items()`.
  Файлы в `media_cache/` удалять только **после** `COMMIT`, чтобы откат транзакции не оставил записи без файлов.
- Редактор баз (`handlers/content.php`, `database_definitions/open/validate/change`): оставь API тем же (`database`,
  `collection`, `index`, `expected`, ответ `count`). `index` — это позиция в списке по `position`. Сверку `expected`
  выполняй внутри `db_tx`, при несовпадении бросай `DomainException`, как сейчас. Убери поле `'file'` из определений
  и читай через функции из таблицы выше. Сообщение «битый JSON» больше не нужно.
- `handle_account_info` (`handlers/account.php:64`) показывает файлы папки пользователя и их описания
  (`ACCOUNT_FILE_INFO`). Перепиши: вместо `pool.json`, `history.json`, `settings.json`, `connections.json`, `last-seen`
  показывай строки «Пул: N записей», «История: N», «Настройки», «Подключения», «Последний заход», а реальные файлы
  (журнал парсера) оставь как есть. Формат элементов массива (`name`, `size`, `modified`, `about`) не меняй, `size`
  для таблиц можно считать как длину JSON.
- `account_web_exposure()` проверяет, что данные не отдаются по HTTP. Замени `sources_db.json` на `data/posta.sqlite`.
- Демо-режим (`?debug=1`, `demo_route`) не должен обращаться к БД. Проверь, что `demo_route` вызывается раньше, чем что-либо вызывает `db()`.
- `reset_project_data()` (`lib/content.php:149`) очищает пул и историю пользователя через `db_tx`.
- `tools/cli.php`: все команды, которые перебирают `users/*/pool.json` и `history.json` (`check-threads`,
  `backfill-*`, `engagement-report`, `rescore-sources`), переведи на новые функции. `engagement-report [файлы]`
  принимает пути к JSON-файлам. Оставь поддержку файлов (это экспорт), а без аргументов бери историю из БД.

---

## 5. Что НЕ переносить (остаётся файлами)

- **Межпроцессные блокировки** через `try_lock` и `flock`: слоты парсера (`users/.slot-N.lock`), блокировка подбора
  (`.discovery-*.lock`), `with_parser_lock`, перезапуск Ollama (`posta-ollama-recycle.lock`). Они защищают
  процессы, а не данные. Оставь их, но лок-файлы могут жить в `data/locks/` вместо `users/`.
- Журнал парсера `users/<id>/parser-v3.log` и папка `users/<id>/` для него.
- Медиафайлы `media_cache/`, `history_media/`.
- Временные файлы в `sys_get_temp_dir()`: VK-токены, `posta-remote-sizes.json`, `posta-exposure-*`, состояние
  QR-входа `posta-tgu-*`, обмен с `tools/tg_user.py`. Python-скрипт читает и пишет эти файлы сам.
- `secret.key.php`, `config.local.php`.
- `MAIN_MODEL_SLOW_FLAG` можно перенести в `kv`, но это не обязательно.

---

## 6. Скрипт переноса существующих данных

Добавь команду `php tools/cli.php migrate-json [--dry-run]`:
1. Создаёт БД (через `db()`), затем в одном `db_tx` переносит `sources_db.json`, `analysis_cache.json`,
   `screened_cache.json`, `telegram_limits.json`, `telegram_app.json`, `users/.dvach-boards.json` и для каждой папки
   `users/<32 hex>/`: `settings.json`, `pool.json`, `history.json`, `connections.json`, `outbox.json`, `published.json`,
   mtime файлов `last-seen`, `proposals-checked`, `threads-checked`.
2. Идемпотентна: повторный запуск не создаёт дублей (пользователь, у которого уже есть строки, пропускается или
   перезаписывается целиком; выбери одно и напиши в выводе).
3. Печатает по каждой таблице «было в JSON → стало строк». С `--dry-run` только считает.
4. **Не удаляет** JSON-файлы. В конце печатает подсказку, какие файлы можно убрать вручную после проверки.
5. Сравнивает: для каждого пользователя `pool_load($uid)` и `history_load($uid)` после переноса должны совпадать с
   исходным JSON (`==`). Если не совпали, выводит ошибку и возвращает ненулевой код.

Перед запуском на реальных данных сделай резервную копию: `tar czf ../posta-json-backup-$(date +%F).tgz *.json users`.

---

## 7. Сопутствующие правки

- `.gitignore`: добавь `data/*.sqlite*` и `data/locks/`.
- `.htaccess`: добавь `sqlite|sqlite-wal|sqlite-shm` в `FilesMatch` с запретом, `data` в `RedirectMatch 404`.
- `tools/fix-permissions.sh`: файлы `data/posta.sqlite*` должны иметь права `664` и группу `www-data`.
- `README.md`: обнови «Требования», «Настройку» (шаг `php tools/cli.php migrate-json` для старых установок),
  строку про `lib/core.php` в таблице «Структура», добавь `lib/db.php`. Пункт «Пользователи» теперь говорит о таблицах, а не о папках.
- Комментарий в шапке `lib/core.php` («JSON-файлы под блокировкой») исправь.
- В конце удали из `lib/core.php` функции `load_data`, `save_data`, `with_file_lock`, `update_data`, если их больше никто
  не вызывает. Если они остались только для временных файлов из раздела 5, оставь их и переименуй комментарий
  раздела в «JSON-файлы (временные и служебные)».

---

## 8. Порядок работы и коммиты

Сначала создай ветку: `git checkout -b feature/sqlite-storage`.

1. `lib/db.php`, схема, константы, `.htaccess`, `.gitignore`. Проверка: `php -r 'require "bootstrap.php"; var_dump(db_value("PRAGMA user_version"));'` выводит `1`. Коммит.
2. Пользователи и настройки (`users`, `load_settings`, `save_settings`, `user_touch`, `user_ids`, маркеры). Коммит.
3. Пул и история, включая голосование, парсер, `all_users_pool_items`, редактор баз и `reset_project_data`. Коммит.
4. Каталог источников и темы (`load_source_db`, `update_source_db`). Коммит.
5. Кэши анализа и быстрых оценок. Коммит.
6. Telegram: connections, outbox, published, лимиты, ключи приложения. Коммит.
7. Состояние парсера и сессия вкладки (`lib/runtime.php`). Коммит.
8. `tools/cli.php migrate-json`, остальные команды CLI, личный кабинет. Коммит.
9. Чистка: удалить неиспользуемые функции и константы, README, fix-permissions. Коммит.

После этапа 9 убедись, что этот поиск ничего не находит, кроме временных файлов из раздела 5:
```sh
grep -rn -E "load_data|save_data|update_data|with_file_lock|\.json['\"]" lib handlers index.php tools/cli.php
```

---

## 9. Проверка (после каждого этапа)

```sh
for f in $(git ls-files '*.php' | grep -v tg-lib); do php -l "$f" >/dev/null || echo "СИНТАКСИС: $f"; done
php tools/cli.php                      # печатает список команд без ошибок
```

Проверка через API (Apache должен работать). Используй отдельного тестового пользователя, чтобы не трогать реальных:

```sh
U=$(openssl rand -hex 16); B=http://localhost/posta/index.php
curl -s -b "ct_uid=$U" "$B?action=settings"
curl -s -b "ct_uid=$U" "$B?action=account_info"
curl -s -b "ct_uid=$U" "$B?action=topics"
curl -s -b "ct_uid=$U" "$B?action=database_options"
curl -s -b "ct_uid=$U" "$B?action=database_read&database=content_history&collection=liked"
curl -s -b "ct_uid=$U" "$B?action=database_read&database=sources_db&collection=telegram_channels" | head -c 300
curl -s -b "ct_uid=$U" -H 'X-Posta: 1' -H 'Content-Type: application/json' -d '{"filters":{"video_only":true}}' "$B?action=save_filters"
curl -s "$B?action=settings&debug=1"   # демо-режим: не должен создать строку в users
sqlite3 data/posta.sqlite "SELECT count(*) FROM users WHERE id='$U'"   # 1
```
Посмотри обработчики и подставь правильные тела запросов, если они отличаются от примера выше.
Ответы до и после переноса должны совпадать по структуре. Перед этапом 2 сохрани эталонные ответы на старой версии
(`git stash` или отдельный worktree) и сравни их.

Проверка конкурентности (после этапа 3): запусти параллельно 20 запросов `vote` или `save_filters` для одного
пользователя (`xargs -P 20`). Ни один не должен вернуть 500, в `error_log` Apache не должно быть `database is locked`.

Проверка прав (после этапа 1 и в конце): `ls -l data/` показывает группу `www-data` и `rw` для группы у `.sqlite`,
`-wal` и `-shm`. Запусти `php tools/cli.php check-threads --dry-run` от своего пользователя, а затем открой сайт:
сайт должен продолжать писать в базу.

---

## 10. Чего не делать

- Не запускай `migrate-json` без `--dry-run` и не удаляй JSON-файлы, пока пользователь не подтвердит.
- Не используй `sudo` и не перезапускай Apache: попроси пользователя.
- Не меняй структуру записей пула и истории (поля внутри `data`) и не «улучшай» логику отбора, оценки и публикации.
- Не держи транзакцию открытой во время сетевых запросов (Ollama, Telegram, Двач, VK) или ffmpeg. Сначала получи
  данные, потом коротко запиши их в `db_tx`. Если нынешний мутатор `update_data` внутри ходит в сеть, вынеси сетевой
  вызов наружу.
- Не открывай второе соединение PDO в том же процессе.
- Не коммить файлы `.sqlite`.

В конце дай пользователю короткий отчёт: что перенесено, что осталось файлами и почему, результаты проверок
(с выводом, если что-то не прошло), команды, которые пользователю нужно выполнить самому (`apt`, `migrate-json`).
