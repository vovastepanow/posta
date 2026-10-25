# Задача: автоподбор находит источники только в Telegram — починить остальные площадки

Ты работаешь в PHP-проекте `/var/www/html/posta`. Автоподбор по площадкам уже сделан по `SOURCES_DISCOVERY_PROMPT.md`:
`lib/discovery.php` (`run_source_discovery_locked`, `check_discovery_candidate`), `lib/discovery_finders.php`
(`discovery_find_telegram/vk/youtube/tiktok/ok`, `discovery_balance`, `discovery_probe`, `source_linked_sources`),
`lib/tiktok_search.php`, `lib/ytdlp.php`. Владелец: «по многим источникам не ищутся новые источники».

**Итог сейчас:** в `source_suggestions` 137 находок — **все Telegram**. В каталоге: Telegram 29, VK 6, RSS 20
(новостные сайты из засева), ОК 0, TikTok 0, YouTube 0.

## Что выяснено (2026-10-23, по журналу парсера `users/<id>/parser-v3.log` и проверкам — перепроверять не нужно)

Журнал прогона по теме «Бизнес»:
```
-> Поиск Telegram через аккаунт: найдено каналов: 48.
-> VK не подключён — поиск сообществ VK пропущен
-> Поиск YouTube «бизнес»: каналов в ответе: 20.   (×3 запроса = 60 каналов)
-> TikTok не отдал поиск: [...] ERROR: ... Failed to initialize cpuinfo
-> Поиск TikTok «бизнес»: аккаунтов: 0.
-> Одноклассники: групп по теме в лентах разделов: 13.
-> Кандидатов на проверку: 61 (проверю до 40). По площадкам: telegram 42, youtube 1, ok 13, links 5.
... дальше 23 проверки подряд — только Telegram (глубина 0, 1, 2), 7 из них «t.me не отдал страницу канала».
```
И в первой строке журнала: `PHP Warning: preg_match(): Allocation of JIT memory failed, PCRE JIT will be disabled.
This is likely caused by security restrictions.`

### Причина 1. Chrome (TikTok) и Node (yt-dlp для YouTube) не работают в окружении Apache

Парсер запускается из Apache и наследует ограничения его службы systemd (`systemctl show apache2`):
`MemoryDenyWriteExecute=yes`, `RestrictNamespaces=yes`, `PrivateDevices=yes`, `PrivateTmp=yes`,
`ProtectSystem=full`, `ProtectHome=read-only`, `LockPersonality=yes`, `RestrictSUIDSGID=yes`.
- `MemoryDenyWriteExecute` запрещает JIT → **V8 в Chrome и Node падает**, PCRE JIT в PHP отключается (то самое
  предупреждение).
- `RestrictNamespaces` → **песочница Chrome не запускается** («Failed to move to new namespace… Zygote process exited»).

**Воспроизведено без sudo** (`systemd-run --user` с теми же свойствами):
```bash
R="systemd-run --user --quiet --pipe --wait -p MemoryDenyWriteExecute=yes -p RestrictNamespaces=yes"
$R /usr/bin/node -e 'console.log(1)'                 # падает (трассировка V8)
$R /usr/bin/node --jitless -e 'console.log(1)'       # работает
$R google-chrome --headless=new --disable-gpu --dump-dom "data:text/html,<p>hi</p>"          # FATAL zygote
$R google-chrome --headless=new --disable-gpu --no-sandbox --no-zygote --js-flags=--jitless \
   --dump-dom "data:text/html,<p>hi</p>"                                                      # работает
```
С этими флагами **поиск TikTok в ограниченном окружении работает**: `https://www.tiktok.com/search/user?q=бизнес` →
`@anatoly_huang`, `@biznes.bastaym`, `@biznes_spring`, `@biznesvdele`… Скачивание шорта yt-dlp в ограниченном
окружении прошло и так, и с `NODE_OPTIONS=--jitless` (≈20 с) — с флагом надёжнее.
Счётчик пустых ответов TikTok `kv: tiktok_search_empty` уже **2 из 3** — ещё одна неудача, и поиск TikTok встанет на
паузу на сутки (`tiktok_search_pause`).

### Причина 2. Ленты YouTube сейчас отвечают 404/500

```
UCuwBAT41kefIqd0HdIkOVmg  500 404 404      (три запроса подряд, https://www.youtube.com/feeds/videos.xml?channel_id=…)
UCDC1b59ro9ifcPOwVlezu-g  404 404 404
UCnD_UXQp6GHIHzQ-ZNbom3g  404 500 500  … (6 из 6 каналов)
```
Утром те же ленты работали — это сбой/ограничение на стороне YouTube, и он может повторяться. Поэтому из 60
найденных каналов кандидатом стал 1 (`discovery_youtube_candidate` проверяет канал через ленты), и **уже добавленные
YouTube-источники тоже не собираются**.
**Работает** список роликов через yt-dlp (≈2–5 с на канал):
```bash
python3 -I tools/bin/yt-dlp --no-warnings --flat-playlist --playlist-end 5 --extractor-args "youtubetab:approximate_date" \
  --print "%(id)s|%(timestamp)s|%(view_count)s|%(title)s|%(thumbnails.-1.url)s" "https://www.youtube.com/channel/<UC…>/videos"
```
→ у вкладки `/videos` есть даты (`timestamp`, приблизительные — до дня); у вкладки `/shorts` дат **нет** (`NA`), есть
`view_count` и превью. Заголовки приходят **автопереведёнными на английский** («REPLACED EMPLOYEES with AI?» у
русского канала) — нужен русский язык (`--extractor-args "youtube:lang=ru"` и/или заголовок
`Accept-Language: ru-RU,ru` через `--add-headers`; проверь, что помогает).

### Причина 3. Очередь проверок — сплошной Telegram

`discovery_balance` даёт каждой площадке по `DISCOVERY_MIN_CHECKS_PER_PLATFORM = 4` в начале очереди, но потом
каждая принятая Telegram-находка через `$push_front` ставит **в начало** очереди своих «похожих» и «упомянутых» —
снежный ком Telegram вытесняет ОК, YouTube и ссылки. В журнале — 23 проверки подряд только Telegram, 13 групп ОК не
проверены ни разу.

### Причина 4. t.me отвечает через раз

7 из 23 проверок — «t.me не отдал страницу канала — проверю в следующий раз» (три попытки по 2 с подряд). Это
ограничение частоты t.me; такие кандидаты просто теряются в этом прогоне.

### Причина 5. Снежный ком уходит от темы

На глубине 2 по теме «Бизнес» принято «MALUNA | Свечи и косметика» (99%) и «Девичник XSIZE» (57%) — быстрая модель
2B «видит бизнес» в любом магазине. Фильтр ключевых слов (`discovery_keyword_hit`) их пропустил.

### Не причина, но важно

VK не подключён (`vk_is_connected()` = false) — поиск VK пропускается. Это решает владелец кнопкой «Подключить VK»;
в коде ничего не делать, кроме понятного сообщения (уже есть).

## Что делаем

Части по порядку: 0 → A → B → C → D → E → F. После каждой — проверка. Не начинай следующую, пока не прошла
предыдущая. Застрял — остановись и напиши, на чём.

## Общие правила

- **Не делай git commit.** Перед записью в базу: `cp data/posta.sqlite data/posta.sqlite.bak-discovery-fix`.
- **НЕ открывай сайт Posta** (`http://localhost/...`): каждая загрузка без куки создаёт пользователя и запускает
  парсер, который нельзя остановить без sudo. Сторонние сайты — можно.
- **Не трогай systemd, Apache, sudo, php.ini.** Ограничения службы Apache — правильная защита, не ослабляй её;
  приспосабливайся к ним флагами запуска. Проверяй всё **в том же окружении**, что у парсера: команды запускай
  через `systemd-run --user --quiet --pipe --wait -p MemoryDenyWriteExecute=yes -p RestrictNamespaces=yes
  -p PrivateTmp=yes …` (свойства, которые пользовательский systemd не даёт выставить, пропусти — напиши какие).
- **Модели:** в проверках — не больше 15 оценок кандидатов за прогон (`SOURCE_DISCOVERY_MAX_CHECKS` передай
  параметром `$limit`).
- **Каталог общий для всех.** Тестовая тема — `тест-котики` (и при необходимости `тест-бизнес`); после — убери их
  записи из каталога, `source_suggestions`, `source_votes`, `discovery.checked_at`, `discovery.runs`.
- Запуск внешних программ — только через `ytdlp_exec` (массив аргументов, без оболочки, таймаут).
- Код в стиле проекта: короткие функции, комментарии по-русски одной строкой.

## Часть 0. Прочитай

`lib/discovery.php` и `lib/discovery_finders.php` целиком; `lib/tiktok_search.php`; `lib/ytdlp.php`
(`ytdlp_exec`, `ytdlp_run`, очередь скачиваний, `ytdlp_run_job`); `lib/rss.php` (`rss_fetch_posts`,
`rss_youtube_shorts_feed`, `rss_known_feed`, разбор YouTube); `lib/tiktok.php`; `lib/ok.php`; `bootstrap.php`;
`config.php` (`DISCOVERY_*`, `SOURCE_DISCOVERY_*`, `CONTENT_MAX_AGE_DAYS`); последний журнал
`users/*/parser-v3.log`.

## Часть A. Chrome и Node в окружении Apache

1. `ytdlp_exec`: в окружение процесса добавь `NODE_OPTIONS=--jitless` (рядом с `PATH`) — Node внутри yt-dlp не будет
   падать на запрете JIT.
2. `tiktok_search_users`: флаги Chrome `--no-sandbox --no-zygote --js-flags=--jitless`, плюс `--disable-dev-shm-usage`
   и `HOME` в окружении — каталог в данных проекта (рядом с профилем `data/chrome-tiktok`), чтобы Chrome не лез в
   `ProtectHome`. Комментарий одной строкой — почему без песочницы (служба Apache запрещает пространства имён) и чем
   это ограничено: отдельный профиль, только `tiktok.com`, таймаут, один процесс за раз.
3. Отличай **сбой запуска** Chrome от **пустого ответа**: если процесс завершился с ошибкой (не 0, `FATAL`,
   «Zygote», «cpuinfo» без HTML) — пиши в журнал «Chrome не запустился: …» и **не** увеличивай `tiktok_search_empty`
   (иначе поломка запуска ставит поиск на паузу на сутки). Пустым считается только нормально загруженная страница
   без аккаунтов.
4. Сбрось счётчики: `tiktok_search_empty` → 0, `tiktok_search_pause` → удалить (через `kv_*`, один раз, в отчёте —
   как сделал).
5. В `bootstrap.php` (только для CLI, `PHP_SAPI === 'cli'`): `ini_set('pcre.jit', '0')` — убирает предупреждение в
   начале каждого журнала (PCRE JIT всё равно отключён окружением).

**Проверка (в ограниченном окружении):**
```bash
R="systemd-run --user --quiet --pipe --wait -p MemoryDenyWriteExecute=yes -p RestrictNamespaces=yes"
$R php -r 'require "/var/www/html/posta/bootstrap.php"; print_r(tiktok_search_users("котики"));'          # ≥ 3 имён
$R php -r 'require "/var/www/html/posta/bootstrap.php"; $r = ytdlp_run(["--version"], 20); var_dump($r["ok"]);'
```
и скачивание одного шорта через функцию очереди (`ytdlp_run_job` или `ytdlp_download`) — файл в кэше, `h264`.
Без ограничений (обычный запуск) — тоже работает.

## Часть B. YouTube без лент RSS

1. В `lib/rss.php` (или новом `lib/youtube.php`): `youtube_channel_posts(string $channel_id, string $tab, int $count): array`
   — список через yt-dlp (`--flat-playlist --playlist-end N`, `youtubetab:approximate_date`, русский язык — проверь,
   какой из способов реально даёт русские заголовки), в формате постов RSS/YouTube из `rss_parse` (`image` — превью
   максимального размера, `text` — заголовок, `posted_at`, `url`, для шортсов `video_via='ytdlp'`,
   `video_page='https://www.youtube.com/shorts/<id>'`). Кэш ответа в `kv` на 30 минут по каналу и вкладке.
2. **Даты шортсов:** во вкладке `/shorts` дат нет. Возьми даты для первых 3 шортсов одним вызовом yt-dlp без
   скачивания (несколько адресов в одном запуске, `--print "%(id)s|%(timestamp)s"`), остальным — дата не новее
   предыдущего (порядок во вкладке — от новых к старым). Замерь время; если > 20 с на канал — бери даты только у
   первого шорта и в отчёте напиши.
3. `rss_fetch_posts` для лент YouTube (`feeds/videos.xml?channel_id=…` и `playlist_id=UUSH…`): сначала лента; если
   ответ не 200 — **запасной путь** `youtube_channel_posts` (вкладка `/videos` или `/shorts` по виду ленты). В журнале
   одна строка «YouTube: лента недоступна (404), список роликов взят через yt-dlp».
4. `discovery_youtube_candidate`: проверка канала — через `youtube_channel_posts` (сначала `/shorts`: есть свежие
   шортсы → кандидат — лента шортсов; иначе `/videos` с ≥ 3 роликами за `CONTENT_MAX_AGE_DAYS`), без лент.
   Ограничь число проверяемых каналов `DISCOVERY_YOUTUBE_MAX` и общим временем YouTube-поиска 90 с.

**Проверка:** `php -r` — по `UCuwBAT41kefIqd0HdIkOVmg` (лента сейчас 404) `rss_fetch_posts` возвращает ролики с
русскими заголовками и датами; `discovery_find_youtube(["котики"])` даёт ≥ 5 кандидатов (было 1 из 60).

## Часть C. Честная очередь проверок

1. Снежный ком (похожие и упомянутые) — **не в начало очереди**, а в свою группу той площадки, с которой ссылка
   (упомянутый в Telegram TikTok-аккаунт — в группу TikTok), и дальше по кругу через `discovery_balance`. Реализуй
   очередь как набор групп по площадкам, из которых проверки берутся по кругу; внутри группы — сначала глубина 0,
   потом 1, потом 2.
2. Порядок круга: Telegram, YouTube, TikTok, ОК, VK, остальные ссылки. Каждая площадка с кандидатами получает
   хотя бы `DISCOVERY_MIN_CHECKS_PER_PLATFORM` проверок, если хватает лимита и времени.
3. В конце прогона — сводка по площадкам одной строкой каждая: «Telegram: найдено 48, проверено 12, принято 4,
   отказ 6, не загрузилось 2», то же для YouTube, TikTok, ОК, VK («не подключён»), ссылок.

**Проверка:** чистая функция порядка — тест в `tools/test_discovery.php` (создай по образцу других
`tools/test_*.php`): 40 Telegram + 2 YouTube + 13 ОК + снежный ком из 10 Telegram после первой принятой — в первых
16 проверках есть все площадки, Telegram из снежного кома не обгоняет ОК.

## Часть D. t.me через раз

1. Запросы к `t.me` — не чаще одного в 1,5 с (общий ограничитель по хосту; если в проекте уже есть
   `host_throttle` — используй его).
2. Кандидат, у которого t.me не отдал страницу, **не теряется**: уходит в конец своей группы (одна повторная попытка
   за прогон); не вышло и тогда — в `kv` «отложенные кандидаты темы», которые следующий прогон проверяет первыми
   (не больше 20, срок 3 дня).

**Проверка:** в тесте — отложенный кандидат попадает в начало следующего прогона и не дублируется.

## Часть E. Снежный ком не уходит от темы

1. Кандидат с глубиной ≥ 1 (похожий / упомянутый) проходит дальше, только если слова темы есть **в названии или
   описании** канала (не только в постах). Кандидаты из прямого поиска — как сейчас.
2. На глубине 2 — порог принятия на 10 пунктов выше (`DISCOVERY_DEEP_EXTRA_SCORE = 10`).
3. Проверь на журнале выше: «MALUNA | Свечи и косметика» и «Девичник XSIZE» по теме «Бизнес» были бы отсеяны
   (воспроизведи функцией `discovery_keyword_hit` на их названиях и описаниях, без модели).

## Часть F. Прогон и отчёт

Полный прогон в ограниченном окружении:
```bash
systemd-run --user --quiet --pipe --wait -p MemoryDenyWriteExecute=yes -p RestrictNamespaces=yes \
  php -r 'require "/var/www/html/posta/bootstrap.php"; app_user_bind("<id пользователя с подключённым Telegram — найди в users.connections>");
  run_source_discovery_locked("тест-котики", 15);'
```
(если `run_source_discovery_locked` требует живую сессию парсера — `parser_session_alive()` — найди, как обойти для
CLI-проверки, не меняя поведение парсера; например, параметр или константа только для CLI.)
Ожидаемо: в сводке есть проверенные кандидаты Telegram, YouTube, TikTok и ОК; хотя бы по двум площадкам кроме
Telegram — «принято ≥ 1». Убери тестовые записи.

## Отчёт

1. Что сделано по частям, файлы; вывод `php tools/test_discovery.php`.
2. Сводка прогона `тест-котики` по площадкам (до/после: до — из журнала выше).
3. Время: поиск TikTok через Chrome без JIT (он медленнее), список YouTube через yt-dlp на канал, даты шортсов.
4. Что владельцу сделать самому: подключить VK (где кнопка), а если TikTok начнёт отдавать капчу — что будет видно в
   журнале.
5. Что не сделано и почему.
