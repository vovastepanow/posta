<?php
// Общая загрузка для API (index.php), сборщика (cron_parser.php) и утилит (tools/cli.php).
require_once __DIR__ . '/config.php';
foreach (['db', 'db_access', 'core', 'auth', 'ai', 'sources', 'topic_tree', 'topics', 'discovery', 'content', 'telegram', 'runtime', 'parser', 'billing', 'lead_radar', 'rss', 'source_add'] as $lib) require_once __DIR__ . "/lib/$lib.php";
