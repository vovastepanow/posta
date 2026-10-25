<?php
// Тред имиджборды и статья как контекст анализа, пороги отбора тредов (IMAGEBOARD_CONTEXT_HASHTAGS_PROMPT.md, часть A).
// Запуск: php tools/test_imageboard_context.php. Без модели и без записи в базу: только чистые функции.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

$failures = 0;
$check = function ($name, $ok) use (&$failures) { echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n"; if (!$ok) $failures++; };

echo "A. Контекст треда:\n";
$reply = ['source_key' => 'dvach:b', 'board' => 'b', 'thread_id' => '100', 'post_id' => '105', 'image' => 'https://cdn.test/r.jpg', 'text' => 'Это >>100 смешно, ахаха', 'captions' => ['Это >>100 смешно, ахаха'],
    'thread_title' => 'Абсурдные картинки тред #47', 'thread_op' => 'Кидайте абсурд', 'is_op' => false, 'source' => 'Двач /b/ · Абсурдные картинки тред #47'];
$block = analysis_context_block($reply);
$check('ответ Двача: блок с названием треда', str_contains($block, 'Тред: «Абсурдные картинки тред #47»'));
$check('ответ Двача: шапка треда в блоке', str_contains($block, 'Шапка треда (о чём тред): Кидайте абсурд.'));
$check('ответ Двача: текст ответа без >>номера', str_contains($block, 'Текст ответа в треде') && !str_contains($block, '>>100'));
$op = array_merge($reply, ['is_op' => true, 'text' => 'Абсурдные картинки тред #47. Кидайте абсурд', 'post_id' => '100']);
$check('ОП-пост: шапка — его текст, строки ответа нет', str_contains(analysis_context_block($op), 'Кидайте абсурд') && !str_contains(analysis_context_block($op), 'Текст ответа'));
$check('4chan: распознаётся по ключу площадки', imageboard_item(['source_key' => '4chan:g:123']));
$check('Telegram: блока нет', analysis_context_block(['source_key' => 'source:мем канал', 'text' => 'Подпись', 'image' => 'x.jpg']) === '');
$check('тред без названия — «без названия»', str_contains(analysis_context_block(array_merge($reply, ['thread_title' => ''])), 'Тред: «без названия»'));

echo "B. Статьи:\n";
$article = ['source_key' => 'source:vc.ru', 'text' => 'Тело статьи про ставку ЦБ', 'image' => '', 'text_post' => true,
    'article' => ['title' => 'ЦБ поднял ставку до 16%', 'lead' => 'Решение неожиданное', 'site' => 'vc.ru', 'rubric' => 'Финансы']];
$text = analysis_text($article);
$check('статья: заголовок первым', str_starts_with($text, 'Заголовок статьи: ЦБ поднял ставку до 16%'));
$check('статья: подзаголовок, рубрика и сайт', str_contains($text, 'Подзаголовок: Решение неожиданное') && str_contains($text, 'Рубрика: Финансы · Сайт: vc.ru'));
$check('статья без заголовка — текст как есть', analysis_text(['text' => ' Просто текст ', 'article' => ['title' => '']]) === 'Просто текст');
$cover = analysis_context_block($article + ['image' => 'https://cdn.test/cover.jpg']);
$check('обложка статьи: блок и пометка «судить по заголовку»', str_contains($cover, 'Заголовок статьи:') && str_contains($cover, 'судить по заголовку'));

echo "C. Пороги тредов:\n";
$check('балл 19 — пропуск', imageboard_thread_gate(19) === 'skip');
$check('балл 20 — как раньше', imageboard_thread_gate(20) === null);
$check('балл 70 — как раньше', imageboard_thread_gate(70) === null);
$check('балл 71 — первым', imageboard_thread_gate(71) === 'trust');
$check('без оценки — как раньше', imageboard_thread_gate(null) === null);
$check('порог «мусор» доверенного треда выше на 10', prescreen_junk_threshold_for(['thread_trust' => true]) === (int)round(PRESCREEN_JUNK_P * 100) + 10);
$check('порог «мусор» обычной записи без изменений', prescreen_junk_threshold_for(['thread_trust' => false]) === (int)round(PRESCREEN_JUNK_P * 100));
$empty = imageboard_thread_scores('тест', 'b', [['id' => '1', 'title' => '', 'op' => '']]);
$check('пустая шапка и безымянный тред — без оценки и без модели', $empty === ['1' => null]);

echo "D. Промпт и очередь:\n";
$p_plain = content_analysis_prompt('Тестовая тема', 'подпись', [], null, '');
$p_ctx = content_analysis_prompt('Тестовая тема', '', [], null, analysis_context_block($reply));
$check('без контекста подпись выводится, блока нет', str_contains($p_plain, 'Подпись поста') && !str_contains($p_plain, 'Тред:'));
$check('с контекстом блок стоит перед «Запись:»', ($at = strpos($p_ctx, 'Тред: «')) !== false && $at < strpos($p_ctx, "\nЗапись:\n"));
$check('с контекстом подпись не дублируется', !str_contains($p_ctx, 'Подпись поста'));
$skipped = [];
$items = [
    ['image' => 'https://cdn.test/a.jpg', 'text' => 'Обычный пост', 'source' => 'test', 'source_key' => 'source:test', 'posted_at' => time() - 60, 'tags' => []],
    ['image' => 'https://cdn.test/b.jpg', 'text' => 'Из доверенного треда', 'source' => 'test', 'source_key' => 'source:test', 'posted_at' => time() - 3600, 'tags' => [], 'thread_trust' => true],
];
$queue = build_candidate_queue(['test' => $items], [], [], [], $skipped, [], null, []);
$check('очередь: пост доверенного треда идёт первым, хотя он старше', ($queue[0]['image'] ?? '') === 'https://cdn.test/b.jpg');

echo $failures ? "\nПровалено: $failures\n" : "\nВсе проверки прошли.\n";
exit($failures ? 1 : 0);
