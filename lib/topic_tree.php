<?php
// Готовое дерево тем: общие ветки и их частные темы. Данные — в lib/taxonomy/<ветка>.php, порядок — в lib/taxonomy/_order.php.
// Засев в реестр — topic_tree_seed() (lib/topics.php), команда seed-topics.

/** Дерево тем: ветки в порядке _order.php (18+ и «Жесть» первыми, «Разное» последней). Кэшируется на запрос. */
function topic_tree(): array {
    static $tree = null;
    return $tree ??= array_map(fn($key) => require __DIR__ . "/taxonomy/$key.php", require __DIR__ . '/taxonomy/_order.php');
}
