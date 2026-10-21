# Задача: прогресс-бар при отправке поста с видео в Telegram

Ты работаешь в PHP-проекте `/var/www/html/postmaker`. Пользователь отправляет пост из редактора (кнопка «Запостить»).
Сейчас прогресс виден только у загрузки файла из браузера на сервер, а дальше — тишина на минуты. Нужно, чтобы во время
отправки была видна полоса: какой идёт этап и сколько процентов.

## Общие правила

- **Сначала прочитай целиком:** `handlers/account.php` (функция `handle_telegram_publish`), `lib/telegram.php`
  (функции `telegram_bot_request`, `telegram_prepare_media`, `telegram_prepare_video`, `telegram_publish_prepared`,
  `telegram_user_schedule`, `telegram_user_run`), `lib/content.php` (функции `ffmpeg_run`, `ffmpeg_binary`),
  `lib/core.php` (функции `http_post`, `user_dir`), `index.php`, `js/editor.js` (функция `submit`), `js/content.js`
  (функция `publishToTelegram`), блок `.pe-progress-*` и блок `.search-progress` в `css/app.css`.
- **Не делай git commit.** Пользователь проверит изменения сам.
- **Не трогай файлы и функции, которые не названы в задаче.** В рабочем дереве есть чужие незакоммиченные правки.
- **Пиши в стиле проекта.** Короткие функции, комментарии по-русски, без классов и лишних абстракций.
- **Не открывай сайт в безголовом браузере и не дёргай curl'ом** `get_current_item`, `tab_open`, `refresh_content`.
  Каждый заход без куки пользователя создаёт нового пользователя и запускает фоновый парсер, который нельзя остановить.
- **Ничего не отправляй в настоящий Telegram.** Живую проверку сделает пользователь.

## Как сейчас идёт отправка

1. Браузер шлёт `POST index.php?action=telegram_publish` с FormData: `text`, `media` (JSON), `scheduled_ts`, `files[]`.
   Если есть файлы с компьютера, `publishToTelegram` в `js/content.js` шлёт его через XMLHttpRequest и отдаёт проценты
   загрузки в `onProgress`. Иначе — через `api()`, без прогресса.
2. Сервер (`handle_telegram_publish`) готовит медиа: `telegram_prepare_media` берёт файл из загрузки, из кэша сайта
   или скачивает по ссылке. Для видео `telegram_prepare_video` при необходимости перекодирует его через `ffmpeg_run`
   (до 10 минут).
3. Пост без отложки уходит ботом: `telegram_publish_prepared` → `telegram_bot_request` → `http_post` (curl),
   файл заливается в Telegram.
4. Отложенный пост уходит через личный аккаунт: `telegram_user_schedule` → `tools/tg_user.py` (Telethon, `send_file`).
5. Только после этого запрос возвращает ответ.

Этапы 2–4 идут внутри одного долгого запроса. Поэтому прогресс сервер пишет в файл, а браузер параллельно опрашивает его
отдельным запросом.

## Часть A. Сервер пишет прогресс в файл

### A1. Файл прогресса

В `lib/telegram.php` добавь два хелпера:

```php
/** Файл прогресса отправки: id приходит от браузера (32 hex-символа). null — id нет или он некорректный. */
function telegram_progress_file($id) { ... }   // user_dir() . "/publish-{$id}.json", id по шаблону /^[a-f0-9]{32}$/

/** Записывает этап и процент (0–100 или null — «неизвестно, идёт»). Не чаще раза в 300 мс, кроме смены этапа. */
function telegram_progress($stage, $percent = null) { ... }
```

- `telegram_progress` пишет JSON `{"stage": "...", "percent": 37, "at": 1792...}` через `file_put_contents(..., LOCK_EX)`.
- Текущий файл хранится в `static`-переменной или в глобальной, которую выставляет `handle_telegram_publish` (см. A5).
  Если файла нет (id не передан), функция ничего не делает. Тогда остальной код работает как раньше.
- Пропускай запись, если этап тот же, процент изменился меньше чем на 1 и прошло меньше 300 мс. Иначе curl будет
  писать на диск сотни раз в секунду.

Этапы — ровно эти строки, интерфейс показывает их как есть:

| stage | Когда |
|---|---|
| `Скачиваю видео` | перед `fetch_public_media` в `telegram_prepare_media`, percent = null |
| `Перекодирую видео` | во время ffmpeg в `telegram_prepare_video`, percent из ffmpeg |
| `Отправка в Telegram` | во время заливки curl'ом или Telethon, percent из байтов |
| `Telegram обрабатывает видео…` | заливка дошла до 100%, ждём ответа Telegram, percent = null |

### A2. Проценты ffmpeg

`ffmpeg_run` в `lib/content.php` не трогай: ей пользуются другие места. Рядом добавь новую функцию:

```php
/** Как ffmpeg_run, но сообщает долю готового: $on_progress(0–100) по out_time_us из -progress и длительности исходника. */
function ffmpeg_run_progress($args, $out, $timeout, $duration, callable $on_progress) { ... }
```

- Запуск через `proc_open` с той же командой, что в `ffmpeg_run`, плюс `-progress pipe:1 -nostats` перед `$args`.
  `timeout $timeout` перед командой сохрани.
- Читай stdout построчно. Строка `out_time_us=12345678` даёт процент: `min(99, out_time_us / 1e6 / $duration * 100)`.
  Если `$duration <= 0`, вызывай `$on_progress(null)` один раз.
- stderr перенаправь в `/dev/null`, как сейчас. Код возврата и проверка `is_file($out) && filesize($out) > 0` — как в `ffmpeg_run`.

В `telegram_prepare_video` замени вызов `ffmpeg_run(...)` на `ffmpeg_run_progress(...)` с теми же аргументами. Длительность
возьми из `telegram_video_info($path)['duration']` (функция уже есть в `lib/telegram.php`). В колбэке вызывай
`telegram_progress('Перекодирую видео', $p)`. Гифки (`gif-$i.mp4` в `telegram_prepare_media`) не трогай.

### A3. Проценты заливки ботом

В `telegram_bot_request` добавь необязательный последний параметр `?callable $on_upload = null`. Если он передан,
добавь к опциям `http_post`:

```php
CURLOPT_NOPROGRESS => false,
CURLOPT_XFERINFOFUNCTION => function ($ch, $down_total, $down_now, $up_total, $up_now) use ($on_upload) {
    if ($up_total > 0) $on_upload($up_now / $up_total * 100);
    return 0;   // 0 — продолжать; не 1, иначе curl оборвёт заливку
},
```

В `telegram_publish_prepared` передай колбэк в оба вызова с файлами (`sendPhoto/sendVideo/sendAnimation` и
`sendMediaGroup`). Колбэк: при `< 100` — `telegram_progress('Отправка в Telegram', $p)`, при `>= 100` —
`telegram_progress('Telegram обрабатывает видео…')`. Вызов `sendMessage` (без файлов) не трогай.

### A4. Проценты заливки через аккаунт (отложенные посты)

- В `telegram_user_schedule` добавь в запрос к питону поле `'progress_file' => <путь из telegram_progress_file или ''>`.
- В `tools/tg_user.py` в обработчике `schedule` передай в оба `client.send_file(...)` параметр
  `progress_callback=...`. Колбэк `(current, total)` пишет в `progress_file` тот же JSON, что `telegram_progress`:
  `{"stage": "Отправка в Telegram", "percent": round(current / total * 100), "at": int(time.time())}`. Не чаще раза
  в 0,3 с. Если поле пустое — колбэк не передавай.
- **Ничего не печатай в stdout:** PHP читает оттуда один JSON-ответ (`sys.stdout.write(json.dumps(result))` в `main`).

### A5. Обработчик отправки и новый маршрут

В `handle_telegram_publish` (`handlers/account.php`):
- прочитай `progress_id` из `$fields` и установи файл прогресса (см. A1);
- в существующем `finally` удали файл прогресса (`@unlink`) рядом с `remove_dir($work)`.

Добавь маршрут опроса:
- в `index.php` — `'telegram_publish_progress'` в массив `GET`;
- там же, рядом со строкой про `proposals_progress`, — ранний выход
  `if ($action === 'telegram_publish_progress') handle_telegram_publish_progress();`, чтобы опрос не загружал данные проекта;
- в `handlers/account.php` — функцию `handle_telegram_publish_progress()`. Она берёт `$_GET['id']`, находит файл через
  `telegram_progress_file` и отвечает `json_response(['stage' => ..., 'percent' => ...])`. Нет файла — `['stage' => null]`.

Проверь, что опрос не ждёт окончания отправки. В `handlers/account.php` есть `session_start` — убедись, что
`handle_telegram_publish` не держит PHP-сессию открытой (`session_write_close()` до долгой работы, если сессия открыта).
Иначе все запросы опроса встанут в очередь за отправкой.

## Часть B. Интерфейс

### B1. Опрос

В `publishToTelegram` (`js/content.js`):
- сгенерируй id: `crypto.randomUUID().replaceAll('-', '')` (32 hex-символа) и добавь в форму `form.append('progress_id', id)`;
- пока идёт запрос отправки, раз в 700 мс делай `api('telegram_publish_progress', { params: { id } })` и передавай
  ответ в `onProgress`. Опрос останавливай в `finally`, при успехе и при ошибке. Ошибки опроса глотай молча;
- путь через XMLHttpRequest (есть файлы) оставь: пока байты уходят на сервер, показывай этап `Загрузка на сервер`
  с процентами из `xhr.upload`. Когда загрузка дошла до 100%, переключайся на данные опроса;
- поменяй контракт `onProgress`: вместо числа он получает `{ stage, percent }`, где `percent` — число или `null`.
  Проверь все места, где вызывается `onProgress` (`grep -n onProgress js/*.js`).

### B2. Полоса в редакторе

В `js/editor.js`, функция `submit`:
- показывай `ui.progress` при **любой** отправке, а не только когда `hasFiles`;
- добавь в `ui.progress` подпись этапа (`span.pe-progress-label`) перед полосой. До первого ответа опроса — текст «Готовлю пост…»;
- `percent` — число: ширина полосы `percent%`, текст справа `NN%`. `percent === null`: полоса бежит без процентов
  (класс `is-indeterminate`), текст справа пустой.

### B3. Стиль

Пользователь хочет **ту же радужную полосу, что у поиска контента** на вкладке «Подбор» (`.search-progress-track` и
`.search-progress-fill` в `css/app.css`). Свой цвет или градиент не придумывай, повтори её один в один. Перепиши блок
`.pe-progress-*`:
- трек высотой **2px**, `border-radius: 999px`, фон `var(--surface-3)`;
- заливка — радужный градиент `linear-gradient(90deg, var(--rainbow)) 0 0 / 200% 100%` с анимацией `search-flow`
  (она уже объявлена в `css/app.css`, новую не создавай);
- `.pe-progress-wrap.is-indeterminate .pe-progress-fill` — ширина 35% и анимация `search-flow` + `search-slide`, как у
  `.search-progress.is-indeterminate`;
- подпись этапа: `font-size: 12px`, `color: var(--muted)`; проценты: `color: var(--text)`, `font-variant-numeric: tabular-nums`.

## Проверка

Выполни по порядку и покажи вывод каждой команды в отчёте.

```
php -l lib/telegram.php
php -l lib/content.php
php -l handlers/account.php
php -l index.php
python3 -m py_compile tools/tg_user.py
for f in js/*.js; do node --input-type=module --check < $f || echo "ОШИБКА в $f"; done
```

**Обязательно `--input-type=module`.** Простой `node --check` пропускает синтаксические ошибки в модулях. Прошлый раз
из-за этого лишняя скобка в `js/content.js` уронила весь сайт.

Проверь проценты ffmpeg на реальном файле без отправки. Кэш медиа лежит вне проекта (`MEDIA_CACHE_DIR` из
`config.local.php`). Найди любое видео: `php -r 'require "bootstrap.php"; echo glob(MEDIA_CACHE_DIR . "/*.mp4")[0] ?? "нет", "\n";'`.

```
php -r 'require "bootstrap.php"; $in = "<путь к mp4>"; $d = telegram_video_info($in)["duration"];
  $ok = ffmpeg_run_progress("-i " . escapeshellarg($in) . " -c:v libx264 -preset veryfast -an -f mp4", "/tmp/claude-progress-test.mp4", 120, $d,
  fn($p) => print("ffmpeg: " . ($p === null ? "?" : round($p)) . "%\n"));
  var_dump($ok); @unlink("/tmp/claude-progress-test.mp4");'
```

Ожидается несколько строк с растущими процентами и `bool(true)`.

Проверь запись и чтение прогресса без отправки:

```
php -r 'require "bootstrap.php"; app_user_bind("b34cfe131b9ddea519d4cbd42c831fb5");
  $id = str_repeat("a", 32); /* установи файл прогресса так же, как в handle_telegram_publish */
  telegram_progress("Отправка в Telegram", 42); echo file_get_contents(telegram_progress_file($id)), "\n";
  var_dump(telegram_progress_file("../../etc/passwd")); @unlink(telegram_progress_file($id));'
```

Ожидается JSON с `"percent":42` и `NULL` для некорректного id.

## Отчёт

В конце коротко напиши:

1. Какие файлы изменены и что в каждом.
2. Вывод всех проверочных команд.
3. Что не получилось или где пришлось отступить от задачи, и почему.
4. Что пользователю проверить вживую: отправить пост с видео и посмотреть, как меняются этапы и проценты.
