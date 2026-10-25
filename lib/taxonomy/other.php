<?php
// Ветка «Разное» — частные темы. Ключи не меняем: на них ссылаются записи. Старые названия — в синонимах.
return [
    'key' => 'other',
    'title' => 'Разное',
    'format' => [
        'media' => 'optional',
        'text' => 'optional',
        'judge' => 'both',
    ],
    'nsfw' => false,
    'about' => 'Не подходит ни под одну тему выше.',
    'aliases' => [],
    'children' => [
        [
            'key' => 'other.misc',
            'title' => 'Прочее',
            'about' => 'Всё, что не подошло никуда.',
            'aliases' => [],
        ],
        [
            'key' => 'other.other',
            'title' => 'Прочее разное',
            'about' => 'Прочее, что не подошло ни к одной другой теме ветки.',
            'aliases' => [],
        ],
    ],
];
