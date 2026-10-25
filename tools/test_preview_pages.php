<?php
// Превью Telegram-каналов: php tools/test_preview_pages.php. Без сети: сетевой вызов подменён, фикстуры из tools/fixtures/.
// Фикстуры telegram_investor4444_p1/p2 — живые страницы t.me/s/investor4444 (первая и ?before=4354), сохранены 2026-10-23.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';
require_once __DIR__ . '/../handlers/sources.php';   // build_source_preview, first_previewable

$failures = 0;
$check = function ($name, $ok) use (&$failures) { echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n"; if (!$ok) $failures++; };
$fixture = fn($name) => file_get_contents(__DIR__ . '/fixtures/' . $name);

// Подмена сети: отдаёт страницы по «до какого поста», считает обращения
$pages = ['' => $fixture('telegram_investor4444_p1.html'), '4354' => $fixture('telegram_investor4444_p2.html')];
$calls = [];
$GLOBALS['telegram_page_fetch_stub'] = function ($channel, $before) use (&$calls, $pages) {
    $calls[] = $before === null ? 'first' : (string)$before;
    return $pages[(string)$before] ?? false;
};
$reset = function (string $channel, array $befores) { foreach ($befores as $b) @unlink(telegram_page_cache_file($channel, $b)); };
$fixture_channel = 'tgfixturepages';
$reset($fixture_channel, [null, 4354]);

echo "A. Пагинация по ?before=:\n";
$feed = telegram_channel_media_posts($fixture_channel, 24, 4);
$check('берёт первую и следующую страницу, третью просит по самому старому посту второй', $calls === ['first', '4354', '4334']);
$check('страниц загружено 2 (третьей нет в подмене)', $feed['pages'] === 2);
$check('постов на страницах: 40', $feed['posts_total'] === 40);
$check('медиа-постов больше одного (с первой страницы 1, со второй ещё)', $feed['media_total'] >= 2);
$check('свежие первыми: номера постов по убыванию', (function () use ($feed) { $ids = array_map(fn($p) => (int)$p['post_id'], $feed['posts']); $desc = $ids; rsort($desc); return $ids === $desc && $ids !== []; })());

$reset($fixture_channel, [null, 4354]); $calls = [];
telegram_channel_media_posts($fixture_channel, 1, 4);
$check('останавливается, когда набрано $want (1 медиа — одна страница)', $calls === ['first']);

$reset($fixture_channel, [null, 4354]); $calls = [];
telegram_channel_media_posts($fixture_channel, 24, 1);
$check('останавливается по $max_pages = 1', $calls === ['first']);

echo "B. Возраст не режет превью, «давно не публикует» остаётся:\n";
// Все даты постов переводим в 2020 год: посты старые, но для превью показываем
$old_page = preg_replace('~datetime="2026-[^"]*"~', 'datetime="2020-01-01T10:00:00+00:00"', $pages['']);
$GLOBALS['telegram_page_fetch_stub'] = fn($channel, $before) => $before === null ? $old_page : false;
$reset('tgoldpages', [null]);
$old = telegram_channel_media_posts('tgoldpages', 24, 1);
$fresh_first = telegram_channel_media_posts($fixture_channel, 24, 1);
$check('посты старше 30 дней попадают в превью (столько же, сколько свежих на той же странице)', count($old['posts']) === count($fresh_first['posts']) && $old['posts'] !== []);
$check('посты действительно старше CONTENT_MAX_AGE_DAYS', $old['posts'] && is_post_outdated($old['posts'][0]['posted_at']));
$info = telegram_page_facts($old_page);
$check('давно не публикует: причина остаётся', str_contains((string)source_inactivity_reason(source_activity($info['post_times'])), 'дн назад'));

echo "C. Текстовые посты и готовность кандидата:\n";
$long = str_repeat('Текст без картинки о бизнесе и продажах. ', 6);
$text_page = '<div class="tgme_widget_message" data-post="tgtextonly/10"><div class="tgme_widget_message_text">Коротко.</div><time datetime="' . date('c', time() - 3600) . '"></time></div>'
    . '<div class="tgme_widget_message" data-post="tgtextonly/11"><div class="tgme_widget_message_text">' . $long . '</div><a class="tgme_widget_message_date"><time datetime="' . date('c', time() - 1800) . '"></time></a></div>';
$GLOBALS['telegram_page_fetch_stub'] = fn($channel, $before) => $before === null ? $text_page : false;
$reset('tgtextonly', [null]);
$text = telegram_channel_media_posts('tgtextonly', 24, 1);
$check('короткий текст (меньше 80 символов) в карточки не попадает', count($text['text_posts']) === 1);   // раздел сбора: text_posts до слияния в ленту
$check('карточка текста: без HTML, ссылка на пост, дата', ($text['text_posts'][0]['url'] ?? '') === 'https://t.me/tgtextonly/11' && ($text['text_posts'][0]['posted_at'] ?? 0) > 0 && !str_contains($text['text_posts'][0]['text'], '<'));
$check('без картинок медиа-постов нет', $text['posts'] === [] && $text['media_total'] === 0);
$cand = ['platform' => PLATFORM_TELEGRAM, 'id' => 'tgtextonly', 'name' => 'Текстовый'];
$preview = build_source_preview($cand, SOURCE_PREVIEW_POSTS);
$check('build_source_preview: текстовый пост в ленте posts, без медиа, с пометкой text_post', count($preview['posts']) === 1 && $preview['posts'][0]['media'] === [] && $preview['posts'][0]['text_post'] === true);
app_user_bind(str_repeat('0', 32));   // пользователь нужен только для видео; у текстового канала видео нет, строк в базе не пишем
$found = first_previewable([$cand]);
$check('текстовый канал годен: first_previewable его не пропускает', is_array($found) && $found['id'] === 'tgtextonly' && count($found['posts']) === 1);

echo "D. Кэш страниц: второй вызов не ходит в сеть:\n";
$reset($fixture_channel, [null, 4354, '4334']); $calls = [];
$GLOBALS['telegram_page_fetch_stub'] = function ($channel, $before) use (&$calls, $pages) { $calls[] = (string)$before; return $pages[(string)$before] ?? false; };
$first = telegram_channel_media_posts($fixture_channel, 24, 4);
$check('кэш: первый вызов сходил в сеть', count($calls) >= 2);
$calls = [];
$GLOBALS['telegram_page_fetch_stub'] = function ($channel, $before) use (&$calls) { $calls[] = (string)$before; return false; };
$second = telegram_channel_media_posts($fixture_channel, 24, 4);
$check('кэш: удачные страницы не загружаются заново (повторно идёт только неудачная третья)', $calls === ['4334']);
$check('кэш: повторный вызов даёт тот же результат', $second['posts_total'] === $first['posts_total'] && count($second['posts']) === count($first['posts']));

echo "E. Неудачная страница не попадает в кэш:\n";
$GLOBALS['telegram_page_fetch_stub'] = fn() => false;
$reset('tgfailing', [null]);
telegram_channel_page_cached('tgfailing');
$check('пустой ответ не сохраняется в кэш', !is_file(telegram_page_cache_file('tgfailing')));

// Уборка: кэш тестовых каналов
$reset($fixture_channel, [null, 4354, '4334']); $reset('tgoldpages', [null]); $reset('tgtextonly', [null]); $reset('tgcachetest', [null, 4354]);
foreach ([$fixture_channel, 'tgoldpages', 'tgtextonly', 'tgcachetest', 'tgfailing'] as $ch) foreach (glob(sys_get_temp_dir() . '/posta-tg-' . md5(strtolower($ch) . '|*') . '*') ?: [] as $f) @unlink($f);

echo $failures ? "\nПровалено: $failures\n" : "\nВсё прошло.\n";
exit($failures ? 1 : 0);
