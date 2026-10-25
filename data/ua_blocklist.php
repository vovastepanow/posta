<?php
// Список блокировки украинского контента (UA_FILTER_PROMPT.md). Правится в админке → «Управление» → «Украинский контент»;
// то, что сохранено там (kv admin:ua_blocklist), главнее этого файла.
// domains — домены и зоны (ua, укр): хост или его родительский домен. sources — идентификаторы как в source_id_for(): telegram:имя, vk:домен…
// Ники каналов сюда не вписывать по памяти: только то, что подтвердил владелец.
return [
    'domains' => ['ua', 'укр', 'pravda.com.ua', 'unian.net', 'unian.ua', 'censor.net', 'korrespondent.net', 'rbc.ua', 'tsn.ua', 'nv.ua', 'liga.net',
        'segodnya.ua', 'ukrinform.ua', 'obozrevatel.com', 'strana.today', 'strana.ua', 'focus.ua', '24tv.ua', 'hromadske.ua', 'suspilne.media', 'epravda.com.ua'],
    'sources' => [],
];
