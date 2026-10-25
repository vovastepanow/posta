<?php
// Новостные и тематические сайты: проверенные ленты RSS с темами дерева. Засев — `php tools/cli.php seed-news-sites`.
// Критерии проверки (2026-10-23): не меньше 10 записей, новейшая не старше 2 суток, картинка у половины записей
// (или у большинства после дообогащения со страницы статьи). Еда: 1000.menu (50 записей, картинки у всех, 2026-10-23).
// Не прошли: kommersant.ru (картинка у 8 из 431),
// cnews.ru (ни одной картинки), banki.ru / ferra.ru (не RSS), eda.rambler.ru (пустая лента), drive.ru / kinopoisk.ru /
// film.ru / travel.ru / gastronom.ru (ленты не объявлены на главной), stroygazeta.ru / remontnik.ru / finam.ru (недоступны).

const NEWS_SITE_MAX_AGE_HOURS = 48;

const NEWS_SITES = [
    ['id' => 'https://vc.ru/rss', 'name' => 'vc.ru', 'topics' => ['Бизнес', 'Маркетинг и реклама', 'Технологии']],
    ['id' => 'https://dtf.ru/rss', 'name' => 'dtf.ru', 'topics' => ['Игры на ПК и консолях', 'Мобильные игры', 'Прочее про игры']],
    ['id' => 'https://habr.com/ru/rss/articles/', 'name' => 'habr.com', 'topics' => ['Программирование', 'Нейросети', 'Прочие технологии']],
    ['id' => 'https://rssexport.rbc.ru/rbcnews/news/30/full.rss', 'name' => 'rbc.ru', 'topics' => ['Экономика', 'Финансы и налоги', 'Мировые события']],
    ['id' => 'https://lenta.ru/rss', 'name' => 'lenta.ru', 'topics' => ['Новости страны и города', 'Мировые события', 'Политика']],
    ['id' => 'https://ria.ru/export/rss2/archive/index.xml', 'name' => 'ria.ru', 'topics' => ['Политика', 'Мировые события']],
    ['id' => 'https://tass.ru/rss/v2.xml', 'name' => 'tass.ru', 'topics' => ['Мировые события', 'Политика', 'Экономика']],
    ['id' => 'https://3dnews.ru/news/rss/', 'name' => '3dnews.ru', 'topics' => ['Гаджеты']],
    ['id' => 'https://www.sports.ru/rss/main.xml', 'name' => 'sports.ru', 'topics' => ['Футбол', 'Прочий спорт']],
    ['id' => 'https://www.vedomosti.ru/rss/news', 'name' => 'vedomosti.ru', 'topics' => ['Экономика', 'Финансы и налоги', 'Инвестиции и трейдинг']],
    ['id' => 'https://nplus1.ru/rss', 'name' => 'nplus1.ru', 'topics' => ['Космос', 'Интересные факты', 'Прочая наука']],
    ['id' => 'https://forklog.com/feed', 'name' => 'forklog.com', 'topics' => ['Криптовалюта']],
    ['id' => 'https://lifehacker.ru/feed/', 'name' => 'lifehacker.ru', 'topics' => ['Психология и коучинг', 'Медицина и здоровье']],
    ['id' => 'https://realty.ria.ru/export/rss2/index.xml', 'name' => 'realty.ria.ru', 'topics' => ['Недвижимость']],
    ['id' => 'https://www.kolesa.ru/export/rss.xml', 'name' => 'kolesa.ru', 'topics' => ['Автомобили', 'Автобизнес']],
    ['id' => 'https://www.zr.ru/rss-news/', 'name' => 'zr.ru', 'topics' => ['Автомобили']],
    ['id' => 'https://smart-lab.ru/rss/index/', 'name' => 'smart-lab.ru', 'topics' => ['Инвестиции и трейдинг']],
    ['id' => 'https://www.ixbt.com/export/rss.xml', 'name' => 'ixbt.com', 'topics' => ['Гаджеты', 'Прочие технологии']],
    ['id' => 'https://naked-science.ru/feed', 'name' => 'naked-science.ru', 'topics' => ['Космос', 'История', 'Интересные факты']],
    ['id' => 'https://www.kinonews.ru/rss/', 'name' => 'kinonews.ru', 'topics' => ['Фильмы', 'Сериалы']],
    ['id' => 'https://1000.menu/feed/news', 'name' => '1000.menu', 'topics' => ['Еда', 'Рецепты', 'Кулинария']],
];

/** Засев каталога лентами сайтов: новые записи, темы — добавляются к существующим; повторный запуск ничего не дублирует. */
function news_sites_seed(): array {
    $counts = ['added' => 0, 'topics' => 0, 'updated' => 0];
    update_source_db(function ($db) use (&$counts) {
        foreach (NEWS_SITES as $site) {
            $found = false;
            foreach ($db[CATALOG_KEYS[PLATFORM_RSS]] as $i => $e) {
                if (strcasecmp((string)($e['id'] ?? ''), $site['id']) !== 0) continue;
                $found = true;
                $have = (array)($e['topics'] ?? []);
                $missing = array_values(array_filter($site['topics'], fn($t) => !in_array($t, $have, true)));
                $db[CATALOG_KEYS[PLATFORM_RSS]][$i]['topics'] = [...$have, ...$missing];
                $db[CATALOG_KEYS[PLATFORM_RSS]][$i]['news'] = true;
                $db[CATALOG_KEYS[PLATFORM_RSS]][$i]['max_age_hours'] = NEWS_SITE_MAX_AGE_HOURS;
                $counts['topics'] += count($missing);
                $counts['updated'] += $missing ? 1 : 0;
            }
            if (!$found) {
                $db[CATALOG_KEYS[PLATFORM_RSS]][] = ['id' => $site['id'], 'name' => $site['name'], 'topics' => $site['topics'], 'news' => true,
                    'max_age_hours' => NEWS_SITE_MAX_AGE_HOURS];
                $counts['added']++;
            }
        }
        return $db;
    });
    return $counts;
}

/** Метка площадки для карточки новостного сайта: «Сайт · vc.ru». */
function news_site_kind(string $feed_url): string {
    return 'Сайт · ' . preg_replace('~^www\.~', '', (string)parse_url($feed_url, PHP_URL_HOST));
}
