// Автопилот источников: состояние, диагностика, история и отчёты.
import { $, DEBUG_MODE, api, el, formatAgo, isTabActive } from './core.js';
import { collectSourcePages } from './source-pages.js';

let latestAutopilotDiagnostic = null;
let autopilotStatusLoading = false;
let autopilotStatusRevision = 0;
let autopilotPendingMutations = 0;
let autopilotMutationQueue = Promise.resolve();
let autopilotSourceDialog = null;
const autopilotSourceStates = { found: 'Найдено за цикл', candidate: 'Ожидают проверки', probation: 'На испытании', active: 'Подтверждено', cooldown: 'Повторная проверка позже', rejected: 'Отклонённые источники' };
const autopilotSourceStateLabels = { candidate: 'Ожидает проверки', probation: 'На испытании', active: 'Подтверждён', cooldown: 'Повторная проверка', rejected: 'Отклонён' };
const autopilotSourceReasonLabels = {
    discovered: 'Ожидает первой проверки', insufficient_fresh_samples: 'Недостаточно публикаций в коротком окне (устаревшая проверка)', sparse_publisher: 'Публикует редко — мониторинг без отклонения',
    feed_unavailable: 'Лента недоступна', feed_access_restricted: 'Площадка ограничила публичный доступ', feed_too_large: 'Ответ ленты превышает лимит размера', rate_limited: 'Ограничение запросов площадки',
    source_identity_recovered: 'Адрес исправлен — проверяется новый источник', invalid_source_identity: 'Повреждён идентификатор источника', unsupported_source_format: 'Формат источника не поддерживается',
    source_not_found: 'Источник не найден', unrecognized_feed: 'Формат ленты не распознан',
    feed_parse_failed: 'Ошибка разбора ленты', preliminary_match_uncertain: 'Недостаточно высокая оценка тематики',
    preliminary_topic_mismatch: 'Проверен и отклонён: не соответствует теме', off_topic_group: 'Группа не соответствует теме',
    assessment_unavailable: 'AI-оценка временно недоступна', probation_full: 'Все места испытания заняты', probation_full_verified: 'Группа проверена; все места испытания заняты', projection_conflict: 'Конфликт подключения',
    shadow_preliminary_pass: 'Прошёл предварительную проверку',
    observed_useful: 'Полезность подтверждена', observed_useful_curation: 'Одобрен как куратор качественных публикаций', insufficient_evidence: 'Недостаточно данных для решения',
    observed_irrelevant: 'Недостаточно публикаций по теме', observed_low_yield: 'Мало полезных публикаций после фильтров', quality_drift: 'Качество снизилось',
    observed_ad_or_duplicates: 'Слишком много рекламы или дублей',
    preliminary_probe_scheduled: 'Назначена повторная диагностика старого отказа',
    yield_readable_trial: 'Испытание по полезной отдаче',
    yield_confirmed: 'Полезная отдача подтверждена новыми публикациями',
    yield_observing: 'Ожидаем новые полезные публикации',
    yield_confirmation_required: 'Нужно подтвердить отдачу новыми публикациями на испытании',
    yield_retain: 'Полезный источник продолжает наблюдаться',
    yield_declined: 'Полезная отдача снизилась',
    yield_no_useful_posts: 'Не найдено полезных постов в оценённой выборке',
    yield_ad_or_self_repeats: 'Слишком много рекламы или собственных повторов'
};
function autopilotSourceIdentity(raw) {
    const source = String(raw || '').trim();
    const prefixes = [
        ['ok:', 'Одноклассники', 'odnoklassniki', 'OK', '#f58220', id => /^\d+$/.test(id) ? 'https://ok.ru/group/' + id : ''],
        ['vk:', 'ВКонтакте', 'vk', 'VK', '#0077ff', id => /^[a-z0-9_.-]+$/i.test(id) ? 'https://vk.com/' + id : ''],
        ['telegram:', 'Telegram', 'telegram', 'TG', '#26a5e4', id => /^[a-z0-9_]+$/i.test(id) ? 'https://t.me/' + id : ''],
        ['tiktok:', 'TikTok', 'tiktok', 'TT', '#171717', id => /^[a-z0-9_.]+$/i.test(id) ? 'https://www.tiktok.com/@' + id : '']
    ];
    const prefixed = prefixes.find(([prefix]) => source.startsWith(prefix));
    if (prefixed) {
        const [prefix, label, icon, fallback, color, toURL] = prefixed;
        const id = source.slice(prefix.length);
        const url = toURL(id);
        return { url, display: url ? url.replace(/^https?:\/\//, '') : source, platform: { label, icon, fallback, color } };
    }
    const rawURL = source.startsWith('rss:') ? source.slice(4) : source;
    let url = '', host = '', pathname = '';
    try {
        const parsed = new URL(rawURL);
        if (['https:', 'http:'].includes(parsed.protocol)) {
            url = parsed.href;
            host = parsed.hostname.toLowerCase();
            pathname = parsed.pathname;
        }
    } catch { /* Unrecognized source IDs stay visible as text. */ }
    const domains = [
        [/^(?:api\.)?vc\.ru$/, 'vc.ru', '', 'VC', '#3268c7'],
        [/^(?:api\.)?dtf\.ru$/, 'DTF', '', 'DTF', '#3268c7'],
        [/^(?:.+\.)?pikabu\.ru$/, 'Пикабу', '', 'П', '#8dc33b'],
        [/^(?:.+\.)?ok\.ru$/, 'Одноклассники', 'odnoklassniki', 'OK', '#f58220'],
        [/^(?:.+\.)?vk\.com$/, 'ВКонтакте', 'vk', 'VK', '#0077ff'],
        [/^(?:.+\.)?(?:t\.me|telegram\.org|telegram\.me)$/, 'Telegram', 'telegram', 'TG', '#26a5e4'],
        [/^(?:.+\.)?(?:youtube\.com|youtu\.be)$/, 'YouTube', 'youtube', 'YT', '#ff0033'],
        [/^(?:.+\.)?tiktok\.com$/, 'TikTok', 'tiktok', 'TT', '#171717'],
        [/^(?:.+\.)?bsky\.app$/, 'Bluesky', 'bluesky', 'BS', '#1185fe'],
        [/^(?:.+\.)?reddit\.com$/, 'Reddit', 'reddit', 'RD', '#ff4500'],
        [/^(?:.+\.)?(?:habr\.com|habrahabr\.ru)$/, 'Хабр', 'habr', 'Х', '#4b7796'],
        [/^(?:.+\.)?tumblr\.com$/, 'Tumblr', 'tumblr', 'T', '#36465d'],
        [/^(?:.+\.)?(?:mastodon\.social|mastodon\.online|mstdn\.social)$/, 'Mastodon', 'mastodon', 'M', '#6364ff'],
        [/^(?:.+\.)?(?:lemmy\.world|lemmy\.ml|lemmy\.zip|lemmy\.today|lemm\.ee)$/, 'Lemmy', 'lemmy', 'L', '#50b879']
    ];
    const match = domains.find(([pattern]) => pattern.test(host));
    const platform = match
        ? { label: match[1], icon: match[2], fallback: match[3], color: match[4] }
        : { label: host || 'Сайт', icon: '', fallback: (host || 'SITE').slice(0, 2).toUpperCase(), color: '#3e4b60', faviconHost: host };
    let display = url ? host + (pathname === '/' ? '' : pathname) : source;
    if (host === 'api.vc.ru' || host === 'api.dtf.ru') {
        const id = new URL(url).searchParams.get('subsitesids') || new URL(url).searchParams.get('subsitesIds');
        display = 'Раздел ' + host.slice(4) + (id ? ' · #' + id : '');
        url = 'https://' + host.slice(4) + '/';
        platform.label = host.slice(4);
    }
    return { url, display: display || source, platform };
}
function autopilotSourceCard(item) {
    const raw = String(item.source_id || '');
    const identity = autopilotSourceIdentity(raw);
    // The server resolves technical feeds to actual community/profile pages.
    // Keep source_id untouched for deduplication and autopilot decisions.
    if (item.url) {
        try {
            const page = new URL(item.url);
            if (page.protocol === 'https:' || page.protocol === 'http:') {
                identity.url = page.href;
                identity.display = page.host + (page.pathname === '/' ? '' : page.pathname);
            }
        } catch { /* Invalid URLs remain non-preferred display links. */ }
    }
    const { platform, url } = identity;
    const sourceKind = String(item.source_kind || '');
    // The top line is always the platform, never the type of resource.
    if (item.platform_label) platform.label = String(item.platform_label);
    // Use the publisher's real name, not an API host, RSS endpoint or
    // technical channel identifier. The server already provides catalog
    // and previously verified public-source titles without extra fetches.
    const displayName = String(item.name || '').trim() || identity.display;
    const icon = el('span', { className: 'autopilot-source-brand', textContent: platform.fallback });
    icon.style.backgroundColor = platform.color;
    // The source artwork is decorative and never clickable. RSS uses the
    // website's actual favicon, not a generic RSS pictogram.
    const imageURL = platform.faviconHost
        ? 'https://www.google.com/s2/favicons?domain=' + encodeURIComponent(platform.faviconHost) + '&sz=128'
        : platform.icon ? 'https://cdn.simpleicons.org/' + platform.icon + '/ffffff' : '';
    if (imageURL) {
        const artwork = el('img', { alt: '', loading: 'lazy', decoding: 'async', src: imageURL });
        artwork.addEventListener('load', () => icon.classList.add('has-artwork'));
        artwork.addEventListener('error', () => artwork.remove());
        icon.append(artwork);
    }
    const name = url ? el('a', {
        href: url, target: '_blank', rel: 'noopener noreferrer', textContent: displayName,
        title: 'Открыть источник в новой вкладке'
    }) : el('span', { textContent: displayName });
    const reason = autopilotSourceReasonLabels[item.reason] || item.reason || 'Пока не проверен';
    // Personal blog is a cross-topic source tag, not an alternative category.
    const personalBlogTag = item.personal_blog === 'yes' && sourceKind !== 'category' && sourceKind !== 'community'
        ? '#личный_блог' : '';
    const formatNote = item.personal_blog === 'unknown' && !sourceKind ? 'Формат автора не определён' : '';
    const checked = item.checked_at ? new Date(item.checked_at * 1000).toLocaleString('ru-RU') : 'ещё не проверялся';
    const next = item.next_check_at ? new Date(item.next_check_at * 1000).toLocaleString('ru-RU') : '';
    return el('article', { className: 'autopilot-source-card' },
        icon,
        el('div', { className: 'autopilot-source-card-body' },
            el('div', { className: 'autopilot-source-card-top' },
                el('span', { className: 'autopilot-source-platform', textContent: platform.label }),
                el('span', { className: 'autopilot-source-state', textContent: autopilotSourceStateLabels[item.state] || item.state || 'Источник' })),
            el('div', { className: 'autopilot-source-card-name' }, name),
            el('div', { className: 'autopilot-source-card-reason', textContent: [reason, personalBlogTag, formatNote].filter(Boolean).join(' · ') }),
            el('div', { className: 'autopilot-source-card-meta' },
                el('span', { textContent: 'Последняя проверка: ' + checked }),
                ...(next ? [el('span', { textContent: 'Следующая: ' + next })] : []))
        ),
        url ? el('a', { className: 'autopilot-source-external', href: url, target: '_blank',
            rel: 'noopener noreferrer', 'aria-label': 'Открыть источник', textContent: '↗' }) : ''
    );
}
function openAutopilotSourceDialog(topic, state, count) {
    if (topic.mode === 'off') return;
    if (autopilotSourceDialog?.dialog.open) {
        if (autopilotSourceDialog.topic === topic.topic && autopilotSourceDialog.state === state && autopilotSourceDialog.mode === topic.mode) {
            autopilotSourceDialog.dialog.focus();
            return;
        }
        autopilotSourceDialog.dialog.close();
    }
    const dialog = el('dialog', { className: 'autopilot-sources-modal' });
    const heading = el('h3', { textContent: autopilotSourceStates[state] || state });
    const subtitle = el('p', { className: 'autopilot-source-subtitle',
        textContent: topic.topic + ' · ' + Number(count || 0).toLocaleString('ru-RU') + ' источников' });
    const close = el('button', { type: 'button', className: 'autopilot-sources-close', textContent: '×', 'aria-label': 'Закрыть' });
    const list = el('div', { className: 'autopilot-source-grid' });
    const hint = el('p', { className: 'autopilot-source-hint',
        textContent: 'Источники могут переходить между этапами во время проверки. Уже загруженные карточки остаются на экране.' });
    const reload = el('button', { type: 'button', className: 'secondary-button', textContent: 'Обновить список' });
    const status = el('span', { className: 'autopilot-source-progress' });
    dialog.append(
        el('div', { className: 'autopilot-sources-header' },
            el('div', {}, heading, subtitle), close),
        hint, list,
        el('div', { className: 'autopilot-sources-footer' }, reload, status));
    document.body.append(dialog);
    const context = { dialog, topic: topic.topic, state, mode: topic.mode };
    autopilotSourceDialog = context;
    let version = 0;
    let controller;
    const loadAllSources = async () => {
        const current = ++version;
        controller?.abort();
        controller = new AbortController();
        reload.disabled = true;
        status.textContent = 'Загрузка…';
        // Keep the current cards visible during refresh, including on errors.
        if (!list.children.length) list.append(el('p', { className: 'autopilot-source-empty', textContent: 'Загружаю источники…' }));
        try {
            const { sources, snapshotAvailable } = await collectSourcePages(
                after => api('source_autopilot_sources', {
                    params: { topic: topic.topic, mode: topic.mode, state, after, limit: 500 },
                    signal: controller.signal
                }), { state, signal: controller.signal });
            if (current !== version || !dialog.open) return;
            list.replaceChildren();
            if (!snapshotAvailable) {
                list.append(el('p', { className: 'autopilot-source-empty',
                    textContent: 'Список последнего поиска появится после следующего прохода: раньше сохранялось только количество.' }));
            } else if (!sources.length) {
                list.append(el('p', { className: 'autopilot-source-empty', textContent: 'В этой секции сейчас нет источников.' }));
            } else {
                list.append(...sources.map(autopilotSourceCard));
            }
            status.textContent = 'Всего показано: ' + sources.length.toLocaleString('ru-RU');
        } catch (error) {
            if (current !== version || !dialog.open) return;
            status.textContent = 'Ошибка загрузки';
            if (!list.querySelector('.autopilot-source-card')) {
                list.replaceChildren(el('p', { className: 'autopilot-source-empty', textContent: 'Не удалось загрузить: ' + error.message }));
            }
        } finally {
            if (current === version) reload.disabled = false;
        }
    };
    const dismiss = () => {
        version++;
        controller?.abort();
        if (autopilotSourceDialog === context) autopilotSourceDialog = null;
        dialog.remove();
    };
    close.addEventListener('click', () => dialog.close());
    dialog.addEventListener('close', dismiss, { once: true });
    reload.addEventListener('click', loadAllSources);
    close.setAttribute('aria-label', 'Закрыть окно источников');
    dialog.setAttribute('aria-label', 'Источники: ' + (autopilotSourceStates[state] || state));
    dialog.showModal();
    loadAllSources();
}

function renderAutopilot(data) {
    latestAutopilotDiagnostic = data;
    renderAutopilotWarmup(data);
    const container = $('autopilot-topics');
    const histories = new Map([...container.querySelectorAll('details[data-autopilot-history]')]
        .map(node => [node.dataset.autopilotHistory, node]));
    container.replaceChildren();
    const backends = (data.backends || []).filter(backend => backend.enabled).map(backend => backend.name).join(', ') || 'не включены';
    $('autopilot-status').textContent = `Версия сервера: ${data.server_revision || 'неизвестна'}. Теневой режим на сервере: ${data.shadow_enabled ? 'включён' : 'выключен'}; автоматический: ${data.auto_enabled ? 'включён' : 'выключен'}. Настроенные площадки: ${backends}.`;
    for (const topic of data.topics || []) {
        const number = value => Number(value || 0).toLocaleString('ru-RU');
        const dateTime = stamp => stamp ? new Date(stamp * 1000).toLocaleString('ru-RU') : 'ещё нет';
        const failureNames = {
            budget_exhausted: 'Исчерпан бюджет одного из ресурсов во время прошлого цикла',
            news_search_disabled: 'Поиск источников отключён',
            news_search_unavailable: 'Новостной поиск недоступен',
            search_unavailable: 'Поисковые площадки не ответили',
            topic_busy: 'Тема занята другим процессом',
            supply_unavailable: 'Не удалось проверить запас',
            classifier_unavailable: 'Модель анализа недоступна',
            restricted_profile: 'Тема не допускает публичный поиск',
            other_error: 'Последний цикл завершился с ошибкой'
        };
        const disabled = topic.mode === 'off' || topic.paused || topic.job_paused ||
            !(topic.mode === 'auto' ? data.auto_enabled : data.shadow_enabled);
        const activePhase = topic.running ? topic.pipeline_phase : '';
        const phaseLabels = {
            prepare: 'Подготовка',
            prepare_legacy: 'Перенос старых предложений',
            prepare_hints: 'Обработка подсказок источников',
            prepare_projection: 'Подключение и очистка источников',
            prepare_delivery: 'Передача проверенных постов',
            search: 'Поиск по площадкам', review: 'Проверка источников', delivery: 'Доставка в пул',
            delivery_projection: 'Сверка источников перед доставкой', delivery_content: 'Доставка проверенного контента'
        };
        const diagnosisNames = {
            classifier_unavailable: 'AI-оценка источников недоступна — проверяю повторно',
            worker_not_running: 'Фоновый обработчик не работает — задания не исполняются',
            worker_disabled_in_dev: 'Фоновые задачи отключены в режиме разработки',
            scheduler_error: 'Ошибка планировщика — задания не запускаются',
            job_missing: 'Задание не создано — проверьте включение режима',
            fetch_budget_exhausted: 'Исчерпан лимит загрузок — повтор после сброса бюджета UTC',
            model_budget_exhausted: 'Исчерпан лимит модели — повтор после сброса бюджета UTC',
            server_fetch_budget_exhausted: 'Исчерпан общий лимит HTTP сервера — повтор после сброса бюджета UTC',
            server_model_budget_exhausted: 'Исчерпан общий лимит AI сервера — повтор после сброса бюджета UTC',
            search_unavailable: 'Поисковики недоступны — назначен повтор',
            supply_unavailable: 'Не удалось оценить запас — назначен повтор',
            topic_busy: 'Тема занята другим процессом',
            no_sources_found: 'Источники пока не найдены',
            sources_under_review: 'Кандидаты проходят проверку'
        };
        const workerFault = ['worker_not_running', 'worker_disabled_in_dev', 'scheduler_error', 'job_missing'].includes(topic.diagnosis);
        const status = disabled ? (topic.paused ? 'На паузе' :
                !(topic.mode === 'auto' ? data.auto_enabled : data.shadow_enabled) ? 'Выключено на сервере' :
                topic.job_paused ? 'Задание отключено' : 'Выключен')
            : workerFault ? diagnosisNames[topic.diagnosis]
            : topic.running ? 'Работает: ' + (phaseLabels[activePhase] || 'запуск цикла')
            : topic.mode === 'auto' && diagnosisNames[topic.diagnosis] ? diagnosisNames[topic.diagnosis]
            : topic.failure ? 'Ошибка на этапе: ' + (phaseLabels[topic.pipeline_phase] || 'выполнение цикла') + ' — назначен повтор'
            : topic.mode === 'shadow' ? 'Теневой режим — без подключения источников'
            : topic.mode === 'auto' && topic.available >= topic.target && topic.target > 0 ? 'Запас собран'
            : 'Ожидает следующего цикла';
        const sourceCandidates = topic.mode === 'auto' ? topic.auto_candidates : topic.shadow_candidates;
        const sourcesReview = topic.mode === 'auto' ? topic.auto_sources_probation : topic.shadow_probation;
        const sourcesActive = topic.mode === 'auto' ? topic.auto_sources_active : topic.shadow_confirmed;
        const sourcesCooldown = topic.mode === 'auto' ? topic.auto_sources_cooldown : topic.shadow_cooldown;
        const sourcesRejected = topic.mode === 'auto' ? topic.auto_sources_rejected : topic.shadow_rejected;
        const stat = (label, value, hint) => el('div', { className: 'autopilot-metric' },
            el('span', { className: 'autopilot-metric-value', textContent: number(value) }),
            el('span', { className: 'autopilot-metric-label', textContent: label }),
            hint ? el('small', { textContent: hint }) : '');
        const scarceServerBudget = [
            [topic.server_fetch_remaining, topic.server_fetch_limit],
            [topic.server_model_remaining, topic.server_model_limit]
        ].some(([remaining, limit]) => Number(limit) > 0 && Number(remaining) / Number(limit) < 0.05);
        const phase = (name, value, detail, state) => el('div', { className: 'autopilot-phase' },
            el('span', { className: 'autopilot-phase-dot ' + state, 'aria-hidden': 'true' }),
            el('div', {}, el('strong', { textContent: name }), el('span', { textContent: value }),
                el('small', { textContent: detail })));
        const sourceStat = (label, value, hint, state) => {
            const metric = stat(label, value, hint);
            metric.classList.add('autopilot-metric-clickable');
            metric.setAttribute('role', 'button');
            metric.setAttribute('tabindex', '0');
            metric.setAttribute('aria-label', 'Открыть источники: ' + label);
            metric.addEventListener('click', () => openAutopilotSourceDialog(topic, state, value));
            metric.addEventListener('keydown', event => {
                if (event.key === 'Enter' || event.key === ' ') {
                    event.preventDefault();
                    openAutopilotSourceDialog(topic, state, value);
                }
            });
            // The copy control is a sibling, not nested in the clickable card,
            // so copying never opens the source dialog or creates nested buttons.
            const copy = el('button', {
                type: 'button', className: 'autopilot-metric-copy',
                textContent: '⧉ Скопировать',
                title: 'Скопировать все источники секции «' + label + '»',
                'aria-label': 'Скопировать источники: ' + label
            });
            copy.addEventListener('click', () => copyAutopilotSection(topic, state, copy));
            return el('div', { className: 'autopilot-metric-group' }, metric, copy);
        };
        const found = Number(topic.last_source_candidates || 0);
        const totalChecked = Number(topic.direct_reviewed_total || 0);
        const failure = failureNames[topic.failure];
        const staleError = failure ? el('p', { className: 'autopilot-alert', textContent:
            'Последняя ошибка: ' + failure + '. Это история предыдущего цикла, а не обязательно текущий лимит.' }) : '';
        const description = el('div', { className: 'autopilot-overview' },
            el('div', { className: 'autopilot-overview-head' },
                el('strong', { textContent: topic.topic }),
                el('span', { className: 'autopilot-state' + (topic.running ? ' is-running' : ''), textContent: status })),
            el('div', { className: 'autopilot-metrics' },
                sourceStat('Найдено за 1 цикл', found, 'Нажмите, чтобы открыть список', 'found'),
                sourceStat('Ожидают проверки', sourceCandidates, 'Нажмите, чтобы открыть список', 'candidate'),
                sourceStat('На испытании', sourcesReview, 'Нажмите, чтобы открыть список', 'probation'),
                sourceStat('Подтверждено', sourcesActive, 'Нажмите, чтобы открыть список', 'active'),
                sourceStat('На повторной проверке', sourcesCooldown, 'Пауза без отклонения; нажмите для списка', 'cooldown'),
                sourceStat('Отклонено', sourcesRejected, 'Проверенные отрицательные решения', 'rejected')),
            el('div', { className: 'autopilot-pipeline', 'aria-label': 'Этапы подбора источников' },
                phase('1. Поиск', topic.last_search_at ? number(found) + ' кандидатов' : 'Нет данных',
                    topic.last_search_at ? 'Последний поиск: ' + dateTime(topic.last_search_at) +
                        ' · площадки: ' + number(topic.last_backends_ok) + ' с результатами, ' +
                        number(topic.last_backends_empty) + ' пустых, ' + number(topic.last_backends_unavailable) + ' недоступны'
                        : 'Поиск ещё не проводился', activePhase === 'search' ? 'active' : topic.last_search_at ? 'done' : 'waiting'),
                phase('2. Проверка источников', number(sourceCandidates) + ' в очереди · ' + number(sourcesReview) + ' на испытании',
                    'Источник подключается после проверки качества; найденный кандидат ещё не одобрен.',
                    activePhase === 'review' ? 'active' : sourcesActive > 0 ? 'done' : sourcesReview > 0 ? 'done' : 'waiting'),
                phase('3. AI-отбор постов', number(totalChecked) + ' проверено',
                    number(topic.direct_accepted_total) + ' принято · ' + number(topic.direct_rejected_total) +
                        ' отклонено. Счётчики относятся к прямому поиску постов.',
                    totalChecked > 0 ? 'done' : 'waiting'),
                phase('4. Контент в пуле', number(topic.available) + ' из ' + number(topic.target) + ' постов',
                    topic.capacity_limited ? 'Цель ограничена вместимостью пула.' :
                        'Это текущий запас публикаций по теме, не число найденных кандидатов.',
                    activePhase === 'delivery' ? 'active' : topic.available > 0 ? 'done' : 'waiting')),
            el('div', { className: 'autopilot-footnotes' },
                el('span', { textContent: 'Сегодня осталось: ' + number(topic.fetch_remaining) +
                    ' HTTP-загрузок · ' + number(topic.model_remaining) + ' AI-вызовов по теме' }),
                el('span', { textContent: 'Сервер: ' + number(topic.server_fetch_remaining ?? 0) +
                    ' из ' + number(topic.server_fetch_limit ?? 0) + ' HTTP · ' +
                    number(topic.server_model_remaining ?? 0) + ' из ' +
                    number(topic.server_model_limit ?? 0) + ' AI' }),
                scarceServerBudget ? el('span', { className: 'autopilot-alert',
                    textContent: 'Внимание: осталось менее 5% одного из ОБЩЕСЕРВЕРНЫХ лимитов. Лимиты одной темы могут быть ещё далеко от исчерпания.' }) : '',
                el('span', { textContent: 'Очередь: ' + number(topic.queue_ready) + ' готовы к проверке · ' +
                    number(topic.queue_delayed) + ' ожидают повторной попытки · ' +
                    number(topic.queue_unchecked) + ' ещё ни разу не проверялись' }),
                Number(topic.reaudit_total || 0) > 0 ? el('span', { textContent: 'Полная переоценка: ' +
                    number(topic.reaudit_checked) + ' из ' + number(topic.reaudit_total) + ' проверено по новым правилам · ' +
                    number(topic.reaudit_ready) + ' готовы к проверке · ' +
                    number(Math.max(0, Number(topic.reaudit_total) - Number(topic.reaudit_checked) - Number(topic.reaudit_ready))) + ' ожидают или недоступны' }) : '',
                el('span', { textContent: 'Причины ожидания: ' + Object.entries(topic.candidate_reasons || {})
                    .sort((a, b) => Number(b[1]) - Number(a[1]))
                    .map(([reason, count]) => (({
                        not_checked: 'ещё не проверены',
                        insufficient_fresh_samples: 'мало свежих публикаций',
                        feed_unavailable: 'лента недоступна',
                        feed_access_restricted: 'доступ ограничен площадкой',
                        feed_too_large: 'слишком большая лента',
                        rate_limited: 'ограничение площадки',
                        source_not_found: 'источник не найден',
                        unrecognized_feed: 'не распознана лента',
                        feed_parse_failed: 'ошибка разбора ленты',
                        preliminary_match_uncertain: 'не прошли порог качества',
                        assessment_unavailable: 'AI-оценка недоступна',
                        projection_conflict: 'конфликт подключения',
                        probation_full: 'старое ограничение испытания',
                        sparse_publisher: 'редко публикуют'
                    })[reason] || reason) + ': ' + number(count)).join(' · ') || 'нет кандидатов' }),
                topic.audit_funnel?.full_recheck?.scheduled ? el('span', { textContent: 'Полная повторная проверка: ' +
                    number(topic.audit_funnel.full_recheck.scheduled) + ' источников поставлено в очередь · ' +
                    number(topic.audit_funnel.full_recheck.waiting_first_check) + ' ожидают первого прохода · ' +
                    number(topic.audit_funnel.full_recheck.preliminary_checked) + ' завершили предварительную стадию · ' +
                    number(topic.audit_funnel.full_recheck.fresh_post_assessment) + ' с новыми вердиктами постов.' }) : '',
                topic.audit_funnel ? el('span', { textContent: 'Воронка сохранённых данных: ' +
                    number(topic.audit_funnel.sources) + ' источников · ' +
                    number(topic.audit_funnel.sources_with_fetch) + ' с успешным чтением · ' +
                    number(topic.audit_funnel.sources_with_post_verdict) + ' с оценками постов · ' +
                    number(topic.audit_funnel.sources_with_eligible) + ' с пригодными постами · ' +
                    number(topic.audit_funnel.sources_with_saved_result) + ' с сохранённым контентом. История ограничена сроком хранения.' }) : '',
                topic.audit_funnel ? el('span', { textContent: 'Разрыв воронки: ' +
                    number(topic.audit_funnel.stages?.fetched_no_post_verdict) + ' прочитаны, но нет оценок постов · ' +
                    number(topic.audit_funnel.stages?.assessed_no_eligible) + ' оценены, но нет пригодных · ' +
                    number(topic.audit_funnel.stages?.eligible_no_saved_result) + ' с пригодными, но без сохранённых результатов · ' +
                    number(topic.audit_funnel.actual_state_changes) + ' фактических смен состояния (не всех записей решений).' }) : '',
                topic.audit_funnel?.preliminary_post_probes?.sources ? el('span', { textContent: 'Повторная диагностика тематического отказа: ' +
                    number(topic.audit_funnel.preliminary_post_probes.sources) + ' источников · ' +
                    number(topic.audit_funnel.preliminary_post_probes.sources_with_eligible) + ' с пригодными постами · ' +
                    number(topic.audit_funnel.preliminary_post_probes.no_text_only_input) + ' без подходящего текстового входа · ' +
                    number(topic.audit_funnel.preliminary_post_probes.provider_failure) + ' с ошибкой модели. Только текст, не независимая разметка.' }) : '',
                topic.audit_funnel?.policy_comparison?.length ? el('span', { textContent: 'Сравнение политик (последняя общая выборка, не эталон): ' +
                    topic.audit_funnel.policy_comparison.map(item => item.strict_next + ' → ' + item.yield_next + ': ' + number(item.sources)).join(' · ') +
                    ' · режим: ' + (topic.yield_policy_enabled ? 'по отдаче (эксперимент)' : 'старый, сравнение в фоне') }) : '',
                topic.yield_pilot_enabled && !topic.yield_policy_enabled && topic.audit_funnel ? el('span', { textContent: 'Ограниченный пилот отдачи: ' +
                    number(topic.audit_funnel.yield_pilot_states?.probation) + ' на испытании · ' +
                    number(topic.audit_funnel.yield_pilot_states?.active) + ' подтверждены · ' +
                    number(topic.audit_funnel.yield_pilot_states?.rejected) + ' отклонены. Только после положительной проверки отдельного поста.' }) : '',
                topic.control_audit ? el('span', { textContent: 'Независимые контрольные издатели SMM: ' +
                    number(topic.control_audit.attempted) + '/' + number(topic.control_audit.panel_size) + ' проверены · ' +
                    number(topic.control_audit.readable) + ' прочитаны · ' +
                    number(topic.control_audit.preliminary_pass) + ' прошли старый порог · ' +
                    number(topic.control_audit.completed) + '/' + number(topic.control_audit.submitted) + ' завершённых оценок постов · ' +
                    number(topic.control_audit.eligible) + ' пригодных · ' +
                    number(topic.control_audit.sources_with_eligible) + ' издателей с пригодными постами. Не подключаются автоматически; не ручная разметка.' }) : '',
                topic.audit_funnel_unavailable ? el('span', { textContent: 'Расширенная диагностическая воронка временно недоступна.' }) : '',
                el('span', { textContent: 'За 24 часа: ' + number(topic.decisions_24h) + ' записанных решений · ' +
                    number(topic.promoted_24h) + ' прошли предварительный отбор · ' +
                    number(topic.rejected_24h) + ' отклонены' }),
                el('span', { textContent: 'За 24 часа проверено: ' + number(topic.checked_24h) +
                    ' источников · ошибок AI-оценки в реестре: ' + number(topic.assessment_unavailable) +
                    ' · мест испытания: ' + number(topic.probation_limit) }),
                el('span', { textContent: 'Фоновый обработчик: ' +
                    (topic.worker_alive ? 'работает' : 'не отвечает') +
                    ' · последний сигнал: ' + dateTime(topic.worker_last_tick) +
                    ' · ошибок запуска: ' + number(topic.worker_panics) +
                    (topic.worker_reconcile_error ? ' · ошибка планировщика' : '') +
                    (topic.job_exists ? '' : ' · задание отсутствует') }),
                el('span', { textContent: 'Запусков задания: ' + number(topic.attempts) +
                    (topic.pipeline_phase_at ? ' · последний этап: ' + dateTime(topic.pipeline_phase_at) : '') }),
                el('span', { textContent: topic.running ? 'Текущий цикл выполняется' :
                    'Следующая проверка: ' + dateTime(topic.next_run_at) })),
            staleError);
        const modeSelect = el('select');
        modeSelect.setAttribute('aria-label', `Режим для темы ${topic.topic}`);
        for (const [value, label] of [['off', 'Выключен'], ['shadow', 'Теневой'], ['auto', 'Автоматический']]) {
            const option = el('option', { value, textContent: label });
            option.selected = topic.mode === value;
            modeSelect.append(option);
        }
        modeSelect.addEventListener('change', async () => {
            modeSelect.disabled = true;
            try {
                await mutateAutopilot('source_autopilot_demand', { topic: topic.topic, mode: modeSelect.value, posts_per_day: topic.posts_per_day, horizon_days: topic.horizon_days });
            } catch (error) { $('autopilot-status').textContent = `Настройки не сохранены: ${error.message}`; modeSelect.value = topic.mode; modeSelect.disabled = false; }
        });
        const blogSelect = el('select');
        blogSelect.setAttribute('aria-label', 'Персональные блоги для темы ' + topic.topic);
        for (const [value, label] of [
            ['any', 'Все типы блогов'],
            ['only', 'Только персональные'],
            ['exclude', 'Исключить персональные']
        ]) {
            const option = el('option', { value, textContent: label });
            option.selected = (topic.personal_blog_mode || 'any') === value;
            blogSelect.append(option);
        }
        blogSelect.addEventListener('change', async () => {
            blogSelect.disabled = true;
            try {
                await mutateAutopilot('source_personal_blog_mode', { topic: topic.topic, mode: blogSelect.value });
            } catch (error) {
                $('autopilot-status').textContent = 'Не удалось сохранить фильтр блогов: ' + error.message;
                blogSelect.value = topic.personal_blog_mode || 'any';
                blogSelect.disabled = false;
            }
        });
        const blogFilter = el('label', { title: 'Фильтр применяется к выдаче постов и сборщику. Неизвестный тип исключается при строгом режиме.' },
            el('span', { textContent: 'Тег #личный_блог: ' }), blogSelect);
        const historyKey = JSON.stringify([topic.topic, topic.mode]);
        const history = histories.get(historyKey) || createAutopilotHistory(topic, historyKey);
        const request = el('button', { className: 'secondary-button', type: 'button', textContent: 'Запросить проверку', disabled: topic.mode === 'off' || topic.paused });
        request.addEventListener('click', async () => {
            request.disabled = true;
            try {
                await mutateAutopilot('source_autopilot_request', { topic: topic.topic });
                $('autopilot-status').textContent = 'Проверка запрошена. Срок повтора и суточные лимиты сохранены.';
            } catch (error) { $('autopilot-status').textContent = `Не удалось запросить проверку: ${error.message}`; request.disabled = false; }
        });
        const pause = el('button', { className: 'secondary-button', type: 'button', textContent: topic.paused ? 'Продолжить' : 'Пауза', disabled: topic.mode === 'off' });
        pause.addEventListener('click', async () => {
            pause.disabled = true;
            try { await mutateAutopilot('source_autopilot_pause', { topic: topic.topic, paused: !topic.paused }); }
            catch (error) { $('autopilot-status').textContent = `Не удалось изменить паузу: ${error.message}`; pause.disabled = false; }
        });
        container.append(el('div', { className: 'autopilot-topic-row' }, description,
            el('div', { className: 'autopilot-topic-actions' }, modeSelect, blogFilter, pause, request), history));
    }
}

function createAutopilotHistory(topic, key) {
    const history = el('details', {}, el('summary', { textContent: 'История решений' }));
    history.hidden = topic.mode === 'off';
    history.dataset.autopilotHistory = key;
    const historyList = el('div', { textContent: 'Откройте историю, чтобы загрузить решения.' });
    const more = el('button', { className: 'secondary-button', type: 'button', textContent: 'Показать ещё', hidden: true });
    history.append(historyList, more);
    let historyLoaded = false, historyLoading = false, before = 0;
    const loadHistoryPage = async () => {
        if (historyLoading) return;
        historyLoading = true;
        more.disabled = true;
        try {
            const result = await api('source_autopilot_history', { params: { topic: topic.topic, mode: topic.mode, before, limit: 20 } });
            if (!historyLoaded) historyList.replaceChildren();
            historyLoaded = true;
            for (const decision of result.decisions || []) {
                const states = { candidate: 'кандидат', probation: 'испытание', active: 'подтверждён', rejected: 'отклонён', cooldown: 'ожидание' };
                const reasons = { discovered: 'найден', shadow_preliminary_pass: 'прошёл предварительную проверку', observed_useful: 'подтверждена полезность', observed_ad_or_duplicates: 'слишком много рекламы или дублей', observed_irrelevant: 'недостаточно материалов по теме', observed_low_yield: 'мало полезных публикаций после фильтров', source_reaudit_v3_scheduled: 'назначена повторная проверка', quality_drift: 'качество снизилось' };
                const text = `${decision.source_id}: ${states[decision.old_state] || 'найден'} → ${states[decision.new_state] || decision.new_state}; ${reasons[decision.reason] || decision.reason}. ${formatAgo(decision.at)}.`;
                historyList.append(el('p', { textContent: text }));
            }
            if (!before && !(result.decisions || []).length) historyList.textContent = 'Решений пока нет.';
            before = result.next_before || 0;
            more.hidden = !before;
        } catch (error) { $('autopilot-status').textContent = `История недоступна: ${error.message}`; }
        finally { historyLoading = false; more.disabled = false; }
    };
    more.addEventListener('click', loadHistoryPage);
    history.addEventListener('toggle', () => {
        if (history.open && !historyLoaded && topic.mode !== 'off') loadHistoryPage();
    });
    return history;
}

function renderAutopilotWarmup(data) {
    const suggestions = $('autopilot-query-suggestions');
    const profiles = data.public_warmup || [];
    const unique = new Set();
    const options = [];
    for (const title of [...profiles.map(p => p.query), ...(data.topics || []).map(p => p.topic)]) {
        const name = String(title || '').trim();
        if (!name || unique.has(name.toLocaleLowerCase('ru-RU'))) continue;
        unique.add(name.toLocaleLowerCase('ru-RU'));
        options.push(el('option', { value: name }));
    }
    suggestions.replaceChildren(...options);
    const details = $('autopilot-warmup');
    details.hidden = profiles.length === 0;
    const complete = profiles.filter(p => p.checked_at && Number(p.ready_count) >= Number(p.target)).length;
    $('autopilot-warmup-summary').textContent = data.public_warmup_enabled && data.auto_enabled
        ? `Общая заготовка: ${complete} из ${profiles.length} запросов обеспечены запасом`
        : 'Общая заготовка выключена';
    const labels = {
        ready: 'Запас сформирован', missing_media: 'Не хватает загруженных медиа',
        duplicates: 'Много повторов', needs_review: 'Источники проверяются',
        no_sources: 'Не хватает источников', needs_content: 'Не хватает подходящих публикаций',
        not_checked: 'Ещё не проверялся'
    };
    $('autopilot-warmup-rows').replaceChildren(...profiles.map(p => {
        const count = Number(p.ready_count || 0);
        const description = !p.checked_at ? 'Ещё не проверялся' : (labels[p.diagnosis] || 'Проверка');
        return el('div', { className: 'autopilot-topic-row' },
            el('div', {}, el('strong', { textContent: p.query }),
                el('div', { textContent: `${count} / ${Number(p.target || 100)} · ${description}` })));
    }));
}

function autopilotDiagnosticReport(data) {
    // Share only aggregate operational facts. Never include source IDs,
    // account details, raw exceptions or credential-bearing URLs.
    return JSON.stringify({
        server_revision: data.server_revision,
        captured_at: new Date().toISOString(),
        auto_enabled: data.auto_enabled,
        shadow_enabled: data.shadow_enabled,
        background_content_enabled: data.background_content_enabled,
        public_warmup_enabled: data.public_warmup_enabled,
        public_warmup: data.public_warmup || [],
        topics: (data.topics || []).map(topic => ({
            topic: topic.topic, mode: topic.mode, paused: topic.paused,
            diagnosis: topic.diagnosis, failure: topic.failure,
            worker_alive: topic.worker_alive, worker_last_tick: topic.worker_last_tick,
            worker_reconcile_error: topic.worker_reconcile_error,
            worker_panics: topic.worker_panics, background_enabled: topic.background_enabled,
            job_exists: topic.job_exists, job_paused: topic.job_paused,
            eligible: topic.eligible, running: topic.running,
            attempts: topic.attempts, next_run_at: topic.next_run_at,
            pipeline_phase: topic.pipeline_phase, pipeline_phase_at: topic.pipeline_phase_at,
            last_search_at: topic.last_search_at, last_search_outcome: topic.last_search_outcome,
            auto_candidates: topic.auto_candidates, auto_sources_probation: topic.auto_sources_probation,
            auto_sources_active: topic.auto_sources_active,
            auto_sources_cooldown: topic.auto_sources_cooldown, auto_sources_rejected: topic.auto_sources_rejected,
            reaudit_total: topic.reaudit_total, reaudit_checked: topic.reaudit_checked, reaudit_ready: topic.reaudit_ready,
            rejected_reasons: topic.rejected_reasons, cooldown_reasons: topic.cooldown_reasons,
            last_source_candidates: topic.last_source_candidates, last_direct_sources: topic.last_direct_sources,
            last_search_backends: topic.last_search_backends, last_backends_ok: topic.last_backends_ok,
            last_backends_empty: topic.last_backends_empty, last_backends_unavailable: topic.last_backends_unavailable,
            direct_reviewed_total: topic.direct_reviewed_total, direct_accepted_total: topic.direct_accepted_total,
            direct_rejected_total: topic.direct_rejected_total, available: topic.available, target: topic.target,
            delivery: topic.delivery,
            rejected_24h: topic.rejected_24h,
            queue_ready: topic.queue_ready, queue_delayed: topic.queue_delayed,
            queue_unchecked: topic.queue_unchecked, checked_24h: topic.checked_24h,
            assessment_unavailable: topic.assessment_unavailable,
            decisions_24h: topic.decisions_24h, promoted_24h: topic.promoted_24h,
            candidate_reasons: topic.candidate_reasons,
            candidate_assessment_causes: topic.candidate_assessment_causes,
            probation_reasons: topic.probation_reasons,
            audit_funnel: topic.audit_funnel,
            audit_funnel_unavailable: topic.audit_funnel_unavailable,
            yield_policy_enabled: topic.yield_policy_enabled,
            yield_pilot_enabled: topic.yield_pilot_enabled,
            control_audit: topic.control_audit,
            fetch_remaining: topic.fetch_remaining, model_remaining: topic.model_remaining,
            fetch_limit: topic.fetch_limit, model_limit: topic.model_limit,
            fetch_used: topic.fetch_used, model_used: topic.model_used,
            budget_resets_at: topic.budget_resets_at,
            server_fetch_remaining: topic.server_fetch_remaining,
            server_model_remaining: topic.server_model_remaining,
            server_fetch_limit: topic.server_fetch_limit,
            server_model_limit: topic.server_model_limit
        }))
    }, null, 2);
}

async function copyAutopilotDiagnostic() {
    const button = $('autopilot-copy-diagnostic');
    button.disabled = true;
    try {
        const data = await api('source_autopilot_status');
        latestAutopilotDiagnostic = data;
        await navigator.clipboard.writeText(autopilotDiagnosticReport(data));
        $('autopilot-status').textContent = 'Диагностика скопирована. Можно вставить её в чат — без команд на сервере.';
    } catch (error) {
        $('autopilot-status').textContent = 'Не удалось скопировать диагностику: ' + error.message;
    } finally {
        button.disabled = false;
    }
}

const autopilotExportStates = ['candidate', 'probation', 'active', 'cooldown', 'rejected'];

// These are public-source rows only, but a malformed legacy URL may still
// contain a credential in its raw identifier. Never copy such values verbatim.
function autopilotExportSafeValue(value) {
    return String(value || '')
        .replace(/:\/\/[^\s/@]+@/g, '://[redacted]@')
        .replace(/([?&](?:access_token|api_key|apikey|key|token|signature|sig|auth|password|secret)=)[^&#\s]*/gi, '$1[redacted]');
}

// Export lifecycle states, not "found": found is a subset of the latest
// discovery cohort and would duplicate sources already included below.
async function autopilotExportSourcesForTopic(topic, states = autopilotExportStates) {
    const sources = new Map();
    let snapshotAvailable = true;
    for (const state of states) {
        const section = await collectSourcePages(after => api('source_autopilot_sources', {
            params: { topic: topic.topic, mode: topic.mode, state, after, limit: 500 }
        }), { state });
        if (!section.snapshotAvailable) snapshotAvailable = false;
        for (const item of section.sources) {
            // A recheck may move a source between states during export.
            sources.set(item.source_id, {
                source_id: autopilotExportSafeValue(item.source_id),
                url: autopilotExportSafeValue(item.url),
                name: item.name || '',
                source_kind: item.source_kind || '',
                state: item.state,
                reason: item.reason || '',
                reason_label: autopilotSourceReasonLabels[item.reason] || item.reason || '',
                personal_blog: item.personal_blog || 'unknown',
                source_tags: item.personal_blog === 'yes' && !['category', 'community'].includes(item.source_kind) ? ['личный_блог'] : [],
                review_evidence: item.review_evidence || {},
                first_seen_at: item.first_seen_at || 0,
                checked_at: item.checked_at || 0,
                next_check_at: item.next_check_at || 0
            });
        }
    }
    const entries = [...sources.values()];
    const counts = Object.fromEntries(autopilotExportStates.map(state => [
        state, entries.filter(item => item.state === state).length
    ]));
    const withPostEvidence = entries.filter(item => Array.isArray(item.review_evidence?.review_samples) &&
        item.review_evidence.review_samples.length > 0).length;
    return {
        topic: topic.topic, mode: topic.mode,
        section: states.length === 1 ? autopilotSourceStates[states[0]] : 'Все источники',
        section_key: states.length === 1 ? states[0] : 'all',
        snapshot_available: snapshotAvailable,
        count: entries.length, counts,
        with_post_evidence: withPostEvidence,
        without_post_evidence: entries.length - withPostEvidence,
        sources: entries
    };
}

// A copy action beside each metric exports exactly that section, including
// its name and all pages. "Found" is a search cohort, not a lifecycle state.
async function copyAutopilotSection(topic, state, button) {
    button.disabled = true;
    const original = button.textContent;
    button.textContent = 'Копирую…';
    try {
        const report = await autopilotExportSourcesForTopic(topic, [state]);
        const data = {
            export_kind: 'posta_autopilot_source_section',
            server_revision: latestAutopilotDiagnostic?.server_revision || '',
            captured_at: new Date().toISOString(),
            ...report
        };
        await navigator.clipboard.writeText(JSON.stringify(data, null, 2));
        $('autopilot-status').textContent = report.snapshot_available
            ? 'Скопировано: «' + report.section + '» — ' + report.count.toLocaleString('ru-RU') +
                ' источников, тема «' + report.topic + '».'
            : 'Для «' + report.section + '» сервер не сохранил список последнего поиска; скопирован отчёт с пометкой об отсутствии снимка.';
    } catch (error) {
        $('autopilot-status').textContent = 'Не удалось скопировать секцию «' +
            (autopilotSourceStates[state] || state) + '»: ' + error.message;
    } finally {
        button.textContent = original;
        button.disabled = false;
    }
}

async function copyAutopilotSources() {
    const button = $('autopilot-copy-sources');
    button.disabled = true;
    const original = button.textContent;
    button.textContent = 'Собираю источники…';
    try {
        const data = await api('source_autopilot_status');
        const topics = (data.topics || []).filter(topic => topic.mode === 'auto' || topic.mode === 'shadow');
        if (!topics.length) throw new Error('Нет тем с настроенным автопилотом.');
        const exported = [];
        for (const topic of topics) {
            $('autopilot-status').textContent = 'Собираю источники: ' + topic.topic + '…';
            exported.push(await autopilotExportSourcesForTopic(topic));
        }
        const text = JSON.stringify({
            export_kind: 'posta_autopilot_public_sources',
            server_revision: data.server_revision || '',
            captured_at: new Date().toISOString(),
            note: 'Публичные источники; статусы могут изменяться во время выгрузки. Технические ошибки — не оценка тематики.',
            topics: exported
        }, null, 2);
        await navigator.clipboard.writeText(text);
        const total = exported.reduce((sum, topic) => sum + topic.count, 0);
        const documented = exported.reduce((sum, topic) => sum + topic.with_post_evidence, 0);
        $('autopilot-status').textContent = 'Скопировано ' + total.toLocaleString('ru-RU') +
            ' источников; с примерами постов: ' + documented.toLocaleString('ru-RU') +
            ', пока без примеров: ' + (total - documented).toLocaleString('ru-RU') +
            '. Вставь аудит в чат для независимой проверки.';
    } catch (error) {
        $('autopilot-status').textContent = 'Не удалось скопировать список источников: ' + error.message;
    } finally {
        button.textContent = original;
        button.disabled = false;
    }
}

// Serialize mutations and invalidate older status polls. A slow read cannot
// undo a successful setting change in the interface.
async function mutateAutopilot(action, body) {
    autopilotStatusRevision++;
    autopilotPendingMutations++;
    const request = autopilotMutationQueue.then(() => api(action, { body }));
    autopilotMutationQueue = request.catch(() => {});
    try {
        renderAutopilot(await request);
    } finally {
        autopilotPendingMutations--;
    }
}

export async function loadAutopilot(reloadTopics = false) {
    if (DEBUG_MODE) { $('autopilot-status').textContent = 'В демо-режиме проверка недоступна.'; return; }
    if (autopilotStatusLoading || autopilotPendingMutations) return;
    autopilotStatusLoading = true;
    const revision = autopilotStatusRevision;
    try {
        const data = await api('source_autopilot_status');
        if (revision === autopilotStatusRevision && !autopilotPendingMutations) renderAutopilot(data);
    } catch (error) {
        if (revision === autopilotStatusRevision) $('autopilot-status').textContent = `Сведения недоступны: ${error.message}`;
    } finally { autopilotStatusLoading = false; }
}

async function saveAutopilot(event) {
    event.preventDefault();
    const button = event.currentTarget.querySelector('button');
    button.disabled = true;
    try {
        const query = $('autopilot-topic').value.trim();
        if (!query) throw new Error('Введите поисковый запрос.');
        await mutateAutopilot('source_autopilot_demand', { topic: query, mode: $('autopilot-mode').value, posts_per_day: Number($('autopilot-rate').value), horizon_days: Number($('autopilot-horizon').value) });
    } catch (error) { $('autopilot-status').textContent = `Настройки не сохранены: ${error.message}`; }
    finally { button.disabled = false; }
}

export function initializeAutopilot() {
    $('autopilot-form').addEventListener('submit', saveAutopilot);
    $('autopilot-copy-diagnostic').addEventListener('click', copyAutopilotDiagnostic);
    $('autopilot-copy-sources').addEventListener('click', copyAutopilotSources);
    window.setInterval(() => { if (isTabActive('source-tab')) loadAutopilot(); }, 30000);
}
