# Задача: радар лидов — отдельная админка владельца

Ты работаешь в PHP-проекте `/var/www/html/posta` (Postå: ИИ подбирает контент по теме и публикует в канал).
«Радар лидов» ищет в Telegram-чатах людей, которым нужен Postå (ищут контент для канала, автопостинг и т. п.).
Радаром пользуется **только владелец сервиса** — это инструмент продаж, а не функция для пользователей.

Сейчас радар встроен вкладкой в приложение и не используется: владелец должен вручную вписать чаты и нажать
«Сохранить», а он этого не сделал — непонятно, какие чаты вписывать, и не видно, что радар делает. Бэкенд при этом
работает (проверено): `lib/lead_radar.php` (`lead_radar_run`, настройки в `kv` `lead_radar:<uid>`), команда
`php tools/cli.php leads-scan <uid>`, `tools/tg_user.py` действие `read_chats`, классификатор
`lib/ollama_classifier.php` (`lead_prefilter`, `lead_classify`).

Нужно:
```
/posta/admin/  — вход по паролю владельца
  1. Аккаунт Telegram, через который работает радар (выбор из подключённых в приложении)
  2. «Найти чаты»: поиск публичных ГРУПП (не каналов) по запросам → галочки → в список радара
  3. «Проверить сейчас» + живой журнал: какие чаты прочитаны, сколько сообщений, что ответила модель
  4. Лиды: текст, чат, автор, ссылки, статусы; «Отсеяно моделью» — для настройки
Из приложения вкладка «Радар лидов» убирается целиком.
```

Делай части по порядку: 0 → A → B → C → D → E → F → G. Не начинай следующую, пока не прошла проверка предыдущей.

## Общие правила

- **Не делай git commit.** Владелец проверит изменения сам.
- **НЕ открывай основной сайт** (`http://localhost/posta/`, `index.php`) ни curl, ни браузером: каждая загрузка
  без куки создаёт нового пользователя и запускает парсер, который нельзя остановить без sudo. Админку проверяй
  через встроенный сервер PHP (см. часть A) — **только** адреса `/admin/…`.
- **Код админки никогда не создаёт и не привязывает пользователей приложения**: не вызывай в нём
  `app_user_from_request()`, `user_touch()`, `ensure_default_project_data()`. Нужный `uid` бери из настроек админки
  и передавай явно (`lead_radar_run($uid)`, `telegram_user_call($uid, …)`).
- **Ollama:** классифицируй только существующим `lead_classify()` (модель `SMART_MODEL`). Не загружай другие модели,
  **не переходи на маленькие (2B) ради скорости** — это решает владелец. В проверках — не больше 4 запросов к модели.
- **Telegram — личный аккаунт владельца.** Реальные вызовы в проверках: один `search_chats` с одним запросом и один
  `read_chats` одного чата (`limit` 30). Не вступай в чаты и ничего не отправляй — ни в коде, ни в проверках.
- **Не трогай `crontab`, systemd, Apache, `php.ini`, sudo.** Строку для cron только покажи в отчёте.
- Код в стиле проекта: короткие функции, комментарии по-русски одной строкой, ошибки — `RuntimeException` с понятным
  русским текстом, SQL через `db()`, `db_one`, `db_all`, `db_exec`, `db_tx`; настройки — `kv_get` / `kv_update`.
- Всё из Telegram (тексты, имена, username, названия чатов) — чужие данные: на странице только `textContent`.
  Ссылки собирает сервер по шаблону `https://t.me/<username>` и `https://t.me/<chat>/<id>`.

## Часть 0. Прочитай целиком

- `lib/lead_radar.php`, `lib/ollama_classifier.php`, `tools/ollama_worker.php`, `TELEGRAM_LEADS_README.md`.
- `tools/tg_user.py` целиком: `describe`, `channel_info`, `find_channel`, `read_chats`, `search_channels`, `run`.
- `lib/telegram.php` — раздел «Личный аккаунт»: `telegram_user_run`, `telegram_user_connection`, `telegram_user_call`,
  `telegram_user_find_channels`, `telegram_user_read_chats`.
- `tools/cli.php` — команда `leads-scan`, справка, как читаются аргументы.
- `handlers/content.php` — `handle_tab_heartbeat` (автозапуск радара), `handle_telegram_leads`,
  `handle_telegram_leads_stats`, `handle_update_telegram_lead`, `handle_leads_settings`, `handle_leads_settings_save`
  (запуск фоновой команды — `exec('/usr/bin/php … &')` около строки 312: повтори этот способ).
- `index.php` — списки `GET`/`POST`, проверка `X-Posta`, порядок вызовов внизу; `bootstrap.php`.
- `.htaccess`: наружу отдаётся только `index.php` (правило `<Files "index.php">` действует в **любой** папке), а
  `.php`, `.json`, `.md`, `.log`, `.lock` закрыты. Значит, админка — `admin/index.php` + статические `admin/radar.js`,
  `admin/radar.css`; никаких других `.php` в `admin/`.
- `lib/db.php` — схема `telegram_lead_queue` и `telegram_leads` (миграция 6 и следующие).
- `index.html` (вкладка `telegram-leads-tab`, кнопка `data-tab="telegram-leads-tab"`, подключение
  `js/telegram_leads.js`, `css/telegram_leads*.css`) и `js/telegram_leads.js`.
- `css/app.css` — первые 40 строк: шрифты и цветовые токены (админку оформи в том же стиле: жёлтый `#FFDA1A`,
  чёрная обводка 2px, тени `4px 4px 0 #111`, шрифт Rubik; тёмная тема — по `prefers-color-scheme`).

## Часть A. Вход владельца

1. **Пароль.** Команда `php tools/cli.php admin-password`: спрашивает пароль дважды без эха (`shell_exec('stty -echo')`
   до чтения из `STDIN`, `stty echo` — в `finally`), минимум 10 символов, сохраняет **только**
   `password_hash($p, PASSWORD_DEFAULT)` в `kv` под ключом `admin:password_hash`. Добавь в справку CLI.
2. **`admin/index.php`** — единственная точка входа: без `action` отдаёт HTML-страницу, с `action` — JSON.
   - `require __DIR__ . '/../bootstrap.php'`. Своя PHP-сессия: `session_name('posta_admin')`, cookie `HttpOnly`,
     `SameSite=Strict`, `Secure` — если запрос по HTTPS, `path` — каталог админки. Срок — 12 часов бездействия.
   - Пароль не задан → страница с одной строкой: «Задайте пароль: `php tools/cli.php admin-password`».
   - `POST action=login` `{password}`: `password_verify`; при успехе `session_regenerate_id(true)`, в сессии
     `admin = true` и `csrf = bin2hex(random_bytes(16))`. Неудачи считай в `kv` `admin:fails:<sha1(IP)>`: после 5 за
     15 минут — 429 «Слишком много попыток, подождите 15 минут». Ответ на неверный пароль — без подробностей.
   - `POST action=logout`.
   - Все остальные действия — только при `admin = true` (иначе 401). Все POST — только с заголовком
     `X-Admin-CSRF`, равным `csrf` из сессии (`hash_equals`), иначе 403. Тело POST — JSON.
   - Заголовки ответа: `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, `Cache-Control: no-store`,
     `Content-Security-Policy: default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:`.
     Поэтому **никакого инлайн-JS и инлайн-стилей** в HTML: всё в `admin/radar.js` и `admin/radar.css`; CSRF-токен
     страница получает действием `GET action=session` (`{admin: bool, csrf}`).
   - Ошибки — JSON `{status: 'error', message}` с понятным текстом; исключения логируй через `error_log`, наружу —
     только `getMessage()` своих `RuntimeException`.
3. Логику держи в `lib/radar_admin.php` (подключай через `require_once` из `admin/index.php`, в `bootstrap.php` не
   добавляй); `admin/index.php` — только разбор запроса и вызовы.

**Проверка:**
```bash
php -l admin/index.php lib/radar_admin.php tools/cli.php
printf 'TestPass12345\nTestPass12345\n' | php tools/cli.php admin-password   # без tty stty может ругнуться — это нормально
php -S 127.0.0.1:8099 -t /var/www/html/posta >/dev/null 2>&1 &  SRV=$!
curl -s -c /tmp/adm.txt -b /tmp/adm.txt 'http://127.0.0.1:8099/admin/index.php?action=session'
curl -s -c /tmp/adm.txt -b /tmp/adm.txt 'http://127.0.0.1:8099/admin/index.php?action=leads'          # → 401
```
Войди (возьми `csrf` из `action=session` после логина), проверь: без `X-Admin-CSRF` POST → 403; 6 неверных паролей
подряд → 429. После всех проверок: `kill $SRV`, удали `/tmp/adm.txt`, удали `kv` `admin:fails:%` и **тестовый
пароль** (`DELETE FROM kv WHERE key = 'admin:password_hash'`) — владелец задаст свой. Проверь, что за всё время
не появилось новых строк в `users` (сравни `SELECT COUNT(*) FROM users` до и после).

## Часть B. Аккаунт радара и настройки

- В `kv` `admin:radar` храни `{account_uid: string|null}` — пользователь приложения, чей подключённый личный
  аккаунт Telegram использует радар. Настройки самого радара остаются, где были: `lead_radar:<account_uid>` (их
  читают `lead_radar_run` и `cli leads-scan`). Лиды — в `telegram_leads` с `user_id = account_uid`.
- `GET action=accounts` → пользователи, у которых `telegram_user_connection($uid) !== null`:
  `[{uid, short: первые 8 символов, created_at, selected}]`. Если подключённый аккаунт ровно один и `account_uid`
  пуст — выбери его автоматически.
- `POST action=account_select {uid}` — только из этого списка.
- `GET action=settings` → `{account_uid, enabled, chats: [{ref, title, members, error}], queries: [...], last_run,
  last_seen, last_error}`. `title`/`members`/`error` по чату — из того, что запомнил последний проход
  (добавь в `lead_radar_run` сохранение `chat_info[ref] = {title, error}` в настройки).
- `POST action=settings_save {enabled, chats: [ref…], queries: [строки]}`: чаты — через существующую
  `lead_radar_validate_chats` (передай строкой через `\n`), до 50 чатов (подними лимит в функции с 30);
  запросы — до 15 строк по 2–60 символов.
- Запросы поиска по умолчанию (если владелец не задал свои): «автопостинг», «чат админов телеграм», «админы
  каналов», «smm чат», «продвижение телеграм канала», «контент для канала», «отложенный постинг», «владельцы
  каналов», «ведение телеграм канала», «реклама в телеграм каналах».

**Проверка:** через `php -S` (как в A) — выбрать аккаунт, сохранить 2 выдуманных чата, прочитать `settings`.
Затем верни настройки как были (запомни `kv` `lead_radar:<uid>` до проверки; если ключа не было — удали его).

## Часть C. Поиск чатов

1. `tools/tg_user.py`: действие `search_chats` `{queries: [...], limit: 30}` — как `search_channels`, но берёт
   **группы**: `types.Channel` с `megagroup = True` (и `gigagroup`) и с `username`. Каналы-витрины (`broadcast`),
   приватные группы без username и `types.Chat` пропускай. Поля: `username`, `title`, `members`
   (`participants_count`, может быть `None` → 0), `query`. `FloodWaitError` — вернуть, что успели.
2. `lib/telegram.php`: `telegram_user_find_chats(array $queries, $uid)` рядом с `telegram_user_find_channels`.
3. Админка: `POST action=chats_search {queries}` → найденные группы, кроме уже добавленных, отсортированы по
   `members`, у каждой — `ref` `@username`, `title`, `members`, `query` (по какому запросу найдена).
   Группы меньше 50 участников отбрасывай.
4. `read_chats` в `tg_user.py`: если чат закрыт для чтения без вступления (`ChannelPrivateError`,
   `ChatAdminRequiredError`, пустая история из-за ограничений) — в `error` чата текст «Закрыт для чтения без
   вступления»; **не вступай** в чат.

**Проверка:** `python3 -I -c "import ast; ast.parse(open('tools/tg_user.py').read())"`; один реальный вызов:
```bash
php -r 'require "bootstrap.php"; require "lib/radar_admin.php";
echo json_encode(telegram_user_find_chats(["чат админов телеграм"], "<account_uid>"), JSON_UNESCAPED_UNICODE|JSON_PRETTY_PRINT);'
```
В ответе группы (не каналы) с username. Запиши в отчёт, сколько нашлось и пример трёх.

## Часть D. «Проверить сейчас» и живой журнал

1. `lead_radar_run($uid, ?callable $log = null)` — добавь журнал шагов (по одной строке):
   «Читаю 5 чатов…», «@chat: 37 новых сообщений» / «@chat: ошибка — …», «Отсеяно без модели: 120, в очередь: 6»,
   по каждому сообщению модели: «Модель (14 с): лид — <причина>» или «… не лид — <причина>» + первые 80 символов
   текста, итог «Готово: просмотрено N, лидов M». CLI `leads-scan` печатает журнал в stdout.
   **Перенеси файл блокировки** из `sys_get_temp_dir()` в `USERS_DIR . '/.radar-' . $uid . '.lock'`: у Apache
   обычно своя приватная `/tmp` (systemd `PrivateTmp`), и блокировка из cron и из админки не была бы общей —
   два прохода пошли бы одновременно.
2. `php tools/cli.php leads-scan <uid> --log=<файл>`: журнал ещё и в файл (перезаписать в начале прохода; строки с
   временем `H:i:s`). Файл: `USERS_DIR . '/.radar-' . $uid . '.log'` (каталог `users/` наружу закрыт).
3. Админка:
   - `POST action=scan_start` → если блокировка `lead_radar_run` свободна (проверь `flock` неблокирующим способом на
     тот же файл блокировки), запусти фоном `leads-scan <uid> --log=…` (способ — как в `handlers/content.php:312`,
     `escapeshellarg` на каждый аргумент). Занято → `{running: true}` без второго запуска.
   - `GET action=scan_log` → `{running: bool, lines: [последние 200 строк]}`.
   - На странице: кнопка «Проверить сейчас», под ней журнал (моноширинный, автопрокрутка). Пока `running` — опрос
     раз в 2 секунды; закончилось — обновить лиды и «Отсеяно моделью».
4. **Автозапуск:** пока админка открыта и радар включён, страница раз в минуту вызывает `scan_start`, если
   `last_run` старше 10 минут (проверку времени делай на сервере в `scan_start` с параметром `auto: true`).
   Круглосуточно — cron (строка в отчёте и README).

**Проверка** (не больше 4 запросов к модели): выбери 1 чат, найденный в части C, сохрани его в настройках радара
(запомни прежние настройки), запусти `php tools/cli.php leads-scan <account_uid> --log=/tmp/radar-test.log` (`read_chats`
возьмёт последние 30 сообщений). Если в очередь попало больше 4 сообщений — ограничь классификацию в этой проверке
(например, временной константой окружения `LEAD_BATCH_OVERRIDE=4`, которую читает `lead_radar_run`; убери её
поддержку после проверки или оставь, если она полезна, и опиши). Проверь журнал. Удали `/tmp/radar-test.log`,
верни настройки.

## Часть E. Лиды и «Отсеяно моделью»

- `GET action=leads {status, offset}` — как `handle_telegram_leads`, но `user_id = account_uid`, по 20; статусы из
  белого списка `new, interested, replied, spam, ignored`. Поля: `id, message, chat_title, sender_name,
  telegram_username, profile_url, message_url, reason, lead_status, created_at`.
- `GET action=stats` — счётчики по статусам + в очереди + всего просмотрено за 24 часа.
- `POST action=lead_status {id, status}` — `UPDATE … WHERE id = ? AND user_id = ?`, не обновилось → 404.
- `GET action=rejected` — последние 30 сообщений из `telegram_lead_queue` этого аккаунта со `status = 'processed'`,
  которые **не** стали лидами: текст (до 300 символов), чат, причина модели (`ollama_result`). Нужны, чтобы владелец
  видел, не отсекает ли модель лишнее.
- Кнопка «Переоценить» у отсеянного → вернуть сообщение в `pending` (`attempts = 0`) — модель посмотрит его в
  следующем проходе.

Страница (`admin/radar.js`, `admin/radar.css`, разметка из `admin/index.php`), сверху вниз:
1. шапка «Радар лидов · Postå», кнопка «Выйти»;
2. «Аккаунт»: выпадающий список аккаунтов, переключатель «Радар включён», время последней проверки и ошибка;
3. «Чаты»: список с названием, участниками, ошибкой чтения и крестиком «убрать»; поле «Добавить @чат»;
   кнопка «Найти чаты» → под ней запросы (textarea, по строке) и результаты с галочками и кнопкой «Добавить
   отмеченные»;
4. «Проверка»: «Проверить сейчас» + журнал;
5. «Лиды»: фильтр статусов, карточки (текст, чат, автор-ссылка, «Открыть сообщение», причина, кнопки статусов),
   «Показать ещё»;
6. «Отсеяно моделью» (свёрнуто по умолчанию, `<details>`).
Пустые состояния — словами по ситуации («Нет аккаунта Telegram: подключите его в приложении → Личный кабинет»,
«Добавьте чаты: нажмите «Найти чаты»», «Лидов пока нет: просмотрено N сообщений»). Вёрстка работает на ширине
телефона (одна колонка, поля 16px).

**Проверка:** `node --check admin/radar.js`; через `php -S` — `leads`, `stats`, `rejected` отвечают JSON;
`lead_status` с чужим `id` → 404. Вставь в базу 1 тестовый лид для `account_uid`, смени ему статус через API,
удали его.

## Часть F. Убрать радар из приложения

- `index.html`: убери кнопку вкладки `data-tab="telegram-leads-tab"`, панель `#telegram-leads-tab` целиком,
  `<script src="js/telegram_leads.js">` и обе ссылки на `css/telegram_leads*.css`. Консоль радара
  (`telegram-leads-console`) — тоже, если она только для радара (проверь, не используется ли где-то ещё).
- Удали `js/telegram_leads.js`, `css/telegram_leads.css`, `css/telegram_leads_panel.css`, если на них больше никто
  не ссылается (`grep -rn`).
- `handlers/content.php`: удали `handle_telegram_leads`, `handle_telegram_leads_stats`, `handle_update_telegram_lead`,
  `handle_leads_settings`, `handle_leads_settings_save` и автозапуск радара из `handle_tab_heartbeat` (сердцебиение
  должно остаться рабочим). В `index.php` убери эти действия из `GET`/`POST`.
- `lib/lead_radar.php`, классификатор, `cli leads-scan`, `tools/ollama_worker.php`, таблицы — **оставь**.
- Если в `js/core.js` или CSS остались селекторы вкладки радара — убери.

**Проверка:** `grep -rn "telegram_leads\|leads_settings\|telegram-leads" index.html index.php js css handlers` — пусто;
`php -l handlers/content.php index.php`; `node --check` по изменённым JS.

## Часть G. README и итог

Перепиши `TELEGRAM_LEADS_README.md` коротко:
- что это и что радар только для владельца;
- первый запуск: `php tools/cli.php admin-password` → открыть `https://<домен>/posta/admin/` (локально —
  `http://localhost/posta/admin/`) → выбрать аккаунт → «Найти чаты» → отметить → «Проверить сейчас»;
- cron для круглосуточной работы (владелец добавляет сам):
  `*/10 * * * * /usr/bin/php /var/www/html/posta/tools/cli.php leads-scan --all >/dev/null 2>&1`
  (проверь, что `--all` берёт пользователей с `enabled = true`, — у владельца это `account_uid`);
- ограничения: модель на CPU (секунды–минута на сообщение), до 15 сообщений за проход, отсев по ключевым словам,
  закрытые чаты без вступления не читаются.

Итоговая проверка:
- `php -l` по всем изменённым PHP, `node --check` по JS, `python3 -I -c "import ast; …"` по `tools/tg_user.py`;
- `grep -n "unsafe-inline\|innerHTML" admin/` — пусто (кроме очистки `innerHTML = ''`, лучше `replaceChildren()`);
- число строк в `users` не изменилось; тестовых лидов, паролей, `admin:fails:*`, временных файлов нет.

## Что сдать

Короткий отчёт владельцу по-русски:
- список изменённых, новых и удалённых файлов (одна строка на файл);
- что нашёл `search_chats` (сколько групп, 3 примера) и результат пробного прохода: сколько сообщений прочитано,
  сколько отсеяно, что ответила модель и за сколько секунд;
- что делать владельцу: задать пароль, открыть админку, найти и добавить 5–10 чатов, нажать «Проверить сейчас»;
- строка для cron (сам не добавляй);
- что не сделано и почему.
