# Задача: распознавать один и тот же мем в разных местах

Ты работаешь в PHP-проекте `/var/www/html/posta`. Парсер собирает картинки и видео из Telegram, VK и Двача,
быстро оценивает их маленькой моделью и лучшие отдаёт на полный анализ.

Сейчас повтор узнаётся только по точному адресу файла или поста. Если один мем перезалили в пять каналов, у него пять
разных адресов, и парсер пять раз гоняет его через модель и может показать пользователю пять раз. Нужно узнавать
картинку по содержимому: даже пережатую, уменьшенную или с другим адресом.

Делай части по порядку: 0 → A → B → C → D. Не начинай следующую, пока проверка предыдущей не прошла.

## Общие правила

- **Сначала прочитай целиком:** `lib/parser.php` (особенно `screened_scores_for_topic`, `source_pass_rates`, `cache_merge`,
  `fetch_candidate_poster`, `prescreen_batch`, `analyze_candidate`, `run_collector`, `build_candidate_queue`,
  `candidate_is_known`, `collect_known_images`), `lib/content.php` (`content_history_record`, `media_cache_path`),
  `lib/db.php` (`db_migrate`), `lib/db_access.php` (`screened_cache_load`, `pool_update`, `history_update`), `lib/core.php`
  (`image_shrink`), `tools/cli.php`, `config.php`.
- **Не делай git commit.** Пользователь проверит изменения сам.
- **Не трогай файлы и функции, которые не названы в задаче.** В рабочем дереве есть чужие незакоммиченные правки.
- **Пиши в стиле проекта.** Короткие функции, комментарии по-русски, без классов и лишних абстракций.
- **Не запускай парсер и модели** (`cron_parser.php`, Ollama). Они идут на слабой видеокарте и мешают пользователю.
- **Не открывай сайт в безголовом браузере и не дёргай curl'ом** `get_current_item`, `tab_open`, `refresh_content`.
  Каждый заход без куки пользователя создаёт нового пользователя и запускает фоновый парсер, который нельзя остановить.
- **Перед первым запуском любого PHP-кода после правок сделай копию базы.** Миграция из части B применится сама при
  первом обращении к базе:
  ```
  sqlite3 data/posta.sqlite ".backup data/posta.sqlite.bak-dedup"
  ```

## Часть 0. Починить кэш быстрых оценок

**Ошибка:** `screened_scores_for_topic()` в `lib/parser.php` возвращает массив с ключами `0, 1, 2…`, потому что
`screened_cache_load()` отдаёт список строк SQLite. Код ждёт ключи-адреса картинок: `isset($rejected[$item['image']])` в
`build_candidate_queue` и `$ctx['cached'][$item['image']]` в `prescreen_batch`. Поэтому «уже отсеяна в прошлых запусках»
и «оценка из кэша» никогда не срабатывают, и модель каждый раз оценивает всё заново.

**Исправление:** в `screened_scores_for_topic` верни `[url => score]`. Например, через `array_column($rows, 'score', 'url')`
по отфильтрованным строкам, с приведением `score` к int. Больше ничего в этой функции не меняй.

**Проверка:**
```
php -l lib/parser.php
php -r 'require "bootstrap.php"; $c = screened_cache_load(); $s = screened_scores_for_topic($c, $c[0]["topic"] ?? "");
  echo json_encode(array_slice($s, 0, 2, true), JSON_UNESCAPED_SLASHES), " всего ", count($s), "\n";'
```
Ключи должны быть адресами `https://…`, значения — числами.

## Часть A. Отпечаток картинки

### A1. Функции

Добавь в конец `lib/content.php` раздел `// ---- Повторы: отпечаток картинки (dHash) ----` с тремя функциями:

```php
/** Отпечаток картинки (dHash 256 бит, 64 hex-символа): одинаков у пережатой, уменьшенной копии. null — не картинка. */
function image_phash($bytes) { ... }

/** Сколько бит из 256 различаются у двух отпечатков. */
function phash_distance($a, $b) { ... }

/** Первый ключ из $known ([отпечаток => что угодно]), чей отпечаток ближе PHASH_SAME_DISTANCE, или null. */
function phash_find($hash, array $known) { ... }
```

**`image_phash`** (только GD, он есть):
1. `@imagecreatefromstring($bytes)`; не вышло — `null`.
2. Уменьши до **17×16** через `imagecreatetruecolor(17, 16)` + `imagecopyresampled`.
3. Для каждого пикселя яркость `0.299 * R + 0.587 * G + 0.114 * B` (цвет через `imagecolorat` и сдвиги `>> 16`, `>> 8`, `& 0xFF`).
4. В каждой из 16 строк сравни 16 пар соседей: бит = 1, если левый пиксель ярче правого. Получится 256 бит.
5. Собери биты в 32 байта и верни `bin2hex(...)` — строка из 64 символов.
6. Освободи изображения через `imagedestroy`.

**`phash_distance`:** `unpack('J4', hex2bin($a))` и то же для `$b` дают по 4 числа по 64 бита. Сумма
`substr_count(decbin($x ^ $y), '1')` по четырём парам. PHP здесь 64-битный, `decbin` отрицательного числа даёт 64 символа —
это правильно. Если длина отпечатка не 64 — верни 256 (максимум, «совсем разные»).

**`phash_find`:** простой перебор, вернуть первый ключ с `phash_distance(...) <= PHASH_SAME_DISTANCE`.

### A2. Константа

В `config.php`, рядом с константами `PRESCREEN_*`, добавь:

```php
const PHASH_SAME_DISTANCE = 20;   // из 256 бит: ближе — та же картинка (пережатая, уменьшенная); подбирается командой phash-report
```

### A3. Проверка части A

```
php -l lib/content.php
php -l config.php
```

Затем проверь устойчивость на реальной картинке из кэша медиа. Кэш лежит вне проекта (`MEDIA_CACHE_DIR`).

```
php -r 'require "bootstrap.php";
$files = array_slice(glob(MEDIA_CACHE_DIR . "/*.jpg") ?: [], 0, 2);
[$a, $b] = array_map("file_get_contents", $files);
$h = image_phash($a);
$small = image_shrink($a, 200, 35, true);                       // уменьшенная и сильно пережатая копия
$img = imagecreatefromstring($a); $w = imagesx($img); $hh = imagesy($img);
$crop = imagecrop($img, ["x" => (int)($w * .03), "y" => (int)($hh * .03), "width" => (int)($w * .94), "height" => (int)($hh * .94)]);
ob_start(); imagejpeg($crop, null, 70); $cropped = ob_get_clean();  // обрезка по 3% с краёв
printf("пережатая: %d, обрезанная: %d, другая картинка: %d (порог %d)\n",
  phash_distance($h, image_phash($small)), phash_distance($h, image_phash($cropped)), phash_distance($h, image_phash($b)), PHASH_SAME_DISTANCE);'
```

Ожидается: пережатая — заметно меньше порога, обрезанная — около порога или меньше, другая — намного больше (обычно 90–150).
Если пережатая выходит больше порога — ищи ошибку в `image_phash`, а не поднимай порог.

## Часть B. Отпечаток хранится с записью

### B1. Миграция

В `lib/db.php`, в массив `$migrations`, добавь версию **2**:

```sql
ALTER TABLE screened_cache ADD COLUMN phash TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS screened_cache_phash ON screened_cache(phash);
```

Версию 1 не трогай. Перед первым запуском сделай копию базы (см. «Общие правила»).

### B2. Запись отпечатка

- `cache_merge` в `lib/parser.php`: в `INSERT OR REPLACE` добавь колонку `phash` со значением `$data['phash'] ?? ''`.
- `screened_cache_load` в `lib/db_access.php`: добавь `phash` в `SELECT`.
- `analyze_candidate` в `lib/parser.php`: в массив записи, где уже есть `'format' =>` и `'tags' =>`, добавь
  `'phash' => image_phash($poster) ?? ''`. `$poster` — это байты превью, они там уже есть.
- `content_history_record` в `lib/content.php`: добавь `'phash' => (string)($item['phash'] ?? '')` рядом с `'tags' =>`.

### B3. Проверка части B

```
php -l lib/db.php
php -l lib/db_access.php
php -l lib/parser.php
php -l lib/content.php
sqlite3 data/posta.sqlite ".backup data/posta.sqlite.bak-dedup"
php -r 'require "bootstrap.php"; db(); echo "версия схемы: ", db_value("PRAGMA user_version"), "\n";'
sqlite3 data/posta.sqlite "PRAGMA table_info(screened_cache)"
```

Ожидается версия 2 и колонка `phash` в списке.

## Часть C. Парсер пропускает повторы

### C1. Известные отпечатки

В `lib/parser.php` рядом с `collect_known_images` добавь:

```php
/** Отпечатки записей пула и истории: [phash => подпись источника] — с чем сравнивать новых кандидатов. */
function collect_known_hashes() { ... }   // pool_load() + history_records(), только непустые 'phash'; подпись — $r['source'] ?? ''
```

В `run_collector` добавь в `$ctx` (там, где он создаётся) два поля:
- `'known_hashes' => collect_known_hashes()`;
- `'screened_hashes'` — `[phash => score]` из `$cache` (он уже загружен через `screened_cache_load()`) для строк, где
  `topic` совпадает с `$key` так же, как в `screened_scores_for_topic` (через `mb_strtolower`), и `phash` не пустой.

В `$ctx['stats']` добавь счётчик `'duplicates' => 0`.

### C2. Проверка в быстрой оценке

В `prescreen_batch`, сразу после того как `fetch_candidate_poster` вернул `$poster`, и **до** проверки
`$ctx['cached'][...]`:

```php
$hash = image_phash($poster) ?? '';
if ($hash !== '' && ($seen = phash_find($hash, $ctx['known_hashes'])) !== null) {
    $ctx['stats']['duplicates']++;
    cli_echo('-> ' . candidate_log_label($item) . ': повтор уже виденной картинки' . ($ctx['known_hashes'][$seen] !== '' ? " (из «{$ctx['known_hashes'][$seen]}»)" : '') . ' — пропуск.');
    continue;
}
if ($hash !== '') $ctx['known_hashes'][$hash] = $item['source'] ?? '';   // вторая копия в этом же запуске — тоже повтор
```

Очередь уже отсортирована «горячие первыми», поэтому из нескольких копий остаётся самая горячая.

Затем, если `$ctx['cached'][$item['image']]` не нашлось, проверь `phash_find($hash, $ctx['screened_hashes'])`. Нашлась
быстрая оценка копии — используй её score **так же, как кэшированную** (та же ветка, что для `$cached`, без запроса к
модели).

Отпечаток должен попасть в кэш быстрых оценок: там, где заполняется `$ctx['new_cache'][$item['image']] = [...]`,
добавь `'phash' => <отпечаток этого кандидата>`. Отпечаток нужно донести до этого места: положи его в элемент
`$to_score[] = [...]` как `'phash' => $hash`.

### C3. Итоги

В `run_collector` в строке после сбора кандидатов (`📌 Тема: …`) ничего не меняй. В итоговой рамке «✅ ПОДБОР КОНТЕНТА
ЗАВЕРШЁН» добавь строку `Повторов пропущено: N` в том же формате, что строка «Отклонено».

### C4. Проверка части C

```
php -l lib/parser.php
php -r 'require "bootstrap.php"; app_user_bind("b34cfe131b9ddea519d4cbd42c831fb5"); echo "известных отпечатков: ", count(collect_known_hashes()), "\n";'
```

Пока записи без отпечатков, число может быть 0 — это нормально, часть D это исправит.

## Часть D. Отпечатки для старых записей и подбор порога

В `tools/cli.php` добавь две команды в стиле существующих `case` и строки в справку в начале файла. Справка печатается
из первых строк файла: поправь число строк в `array_slice(..., 1, N)` в ветке `default`, чтобы новые строки попали в вывод.

### D1. `phash-backfill [--dry-run]`

Для записей пула и истории всех пользователей без `phash` считает отпечаток по **локальному** файлу
`media_cache_path($r['image_local'] ?? '')`. Ничего не скачивай. Нет файла — пропусти. Сохраняй через `pool_update` и
`history_update`, как в существующей функции `tag_content_backfill` в этом же файле (повтори её устройство). Печатает:
`посчитано N, без локального файла M`.

### D2. `phash-report [N]`

Считает отпечатки всех записей пула и истории (из `phash`, если есть, иначе по локальному файлу). Печатает до N (по
умолчанию 30) самых близких пар **разных** картинок (`content_image_key` различается) с расстоянием до
`PHASH_SAME_DISTANCE + 15`. По возрастанию расстояния, в каждой строке: расстояние, два пути к файлам, источники.

### D3. Проверка части D и подбор порога

```
php -l tools/cli.php
php tools/cli.php phash-backfill --dry-run
php tools/cli.php phash-backfill
php tools/cli.php phash-report 30
```

Затем **посмотри глазами** пары из отчёта инструментом Read (он показывает картинки):
- 3–4 пары с расстоянием ≤ `PHASH_SAME_DISTANCE`: это должна быть одна и та же картинка;
- 3–4 пары с расстоянием от `PHASH_SAME_DISTANCE + 1` до `PHASH_SAME_DISTANCE + 15`: это должны быть разные картинки.

**Главный риск — мемы на одном шаблоне** (одна картинка, разные подписи). Они похожи по раскладке и могут попасть под
порог. Если увидишь такую пару ниже порога, уменьши `PHASH_SAME_DISTANCE` так, чтобы она оказалась выше. Ложный повтор
хуже пропущенного: пользователь не увидит хороший мем. В отчёте напиши, какие пары посмотрел и какой порог выбрал.

## Отчёт

В конце коротко напиши:

1. Какие файлы изменены и что в каждом.
2. Вывод всех проверочных команд.
3. Какие пары картинок посмотрел, что на них, какой порог выбран и почему.
4. Что не получилось или где пришлось отступить от задачи, и почему.
5. Что пользователю проверить вживую: в журнале следующего поиска должны появиться строки «повтор уже виденной картинки»
   и итог «Повторов пропущено».
