<?php
// Скопируйте в config.local.php (не попадает в git) и заполните на сервере.
return [
    'VK_CLIENT_ID' => '',        // ID приложения из кабинета VK ID
    'VK_REDIRECT_URI' => '',     // как в настройках VK ID: https://ВАШ_ДОМЕН/index.php?action=vk_oauth_callback
    'VK_SERVICE_TOKEN' => '',    // сервисный ключ приложения VK («Сервисный ключ доступа» на id.vk.com): без входа читает открытые паблики (wall.get)
    'VK_OAUTH_SCOPE' => '',      // необязательно
];

// ---- Соцсети (AUTOPOST_NETWORKS_PROMPT.md, часть C): ключи приложений владельца ----
// Без ключа сеть показывается как «не настроено на сервере». Адрес сайта для возврата из входа — публичный (https):
// define('NETWORK_REDIRECT_BASE', 'https://posta.example.ru');
// define('REDDIT_CLIENT_ID', '');        define('REDDIT_CLIENT_SECRET', '');
// define('TUMBLR_CLIENT_ID', '');        define('TUMBLR_CLIENT_SECRET', '');
// define('LINKEDIN_CLIENT_ID', '');      define('LINKEDIN_CLIENT_SECRET', '');
// define('PINTEREST_CLIENT_ID', '');     define('PINTEREST_CLIENT_SECRET', '');
// define('YOUTUBE_CLIENT_ID', '');       define('YOUTUBE_CLIENT_SECRET', '');
// define('X_CLIENT_ID', '');             define('X_CLIENT_SECRET', '');
// define('OK_APP_KEY', '');
// Instagram, Threads и Facebook (Meta) пока не подключаются: нужно решение владельца (см. отчёт).
