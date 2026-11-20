// Источники: автопилот, каталог, добавление и исключение выбранных источников.
import { $, ask, COUNT, DEBUG_MODE, VIDEO_ICON, cleanCaption, api, bindMenu, chip, el, formatAgo, hide, isTabActive, log, onParserStateChange, onTabOpen, onTopicChange, pluralRu, qualityText, safeSourcePostUrl, setMessage, show, watchParser } from './core.js';
import { BIG_VIDEO_NOTE, closePostView, openPostView, shownText } from './post-view.js';
import { fromSourcePost } from './post-adapters.js';
import { videoPageFor } from './viewer.js';
import { bindFutureDraftButton, toggleFutureDraft, loadCurrentItem, postItemToTelegram } from './content.js';
import { openAccountSection } from './account.js';
import { mediaFileKey } from './media-dedupe.js';
import { loadImage, pageCheck, videoProgress, watchVideo } from './video-pending.js';
import { shortText } from './card.js';
import { initRegistry, loadRegistry } from './registry.js';
import { prepareOwnImage } from './image-prepare.js';

const topicSources = { sources: [], topic: '', filter: 'all', loading: false };

// Retain this exported hook for demo reset callers; manual source cards are gone.
export function clearDiscoveredSource() {}

let latestAutopilotDiagnostic = null;
let autopilotSourceDialog = null;
const autopilotSourceStates = { found: 'Найдено за цикл', candidate: 'Ожидают проверки', probation: 'На испытании', active: 'Подтверждено', cooldown: 'Повторная проверка позже', rejected: 'Отклонённые источники' };
const autopilotSourceStateLabels = { candidate: 'Ожидает проверки', probation: 'На испытании', active: 'Подтверждён', cooldown: 'Повторная проверка', rejected: 'Отклонён' };
const autopilotSourceReasonLabels = {
    discovered: 'Ожидает первой проверки', insufficient_fresh_samples: 'Недостаточно публикаций в коротком окне (устаревшая проверка)', sparse_publisher: 'Публикует редко — мониторинг без отклонения',
    feed_unavailable: 'Лента недоступна', rate_limited: 'Ограничение запросов площадки',
    source_not_found: 'Источник не найден', unrecognized_feed: 'Формат ленты не распознан',
    feed_parse_failed: 'Ошибка разбора ленты', preliminary_match_uncertain: 'Недостаточно высокая оценка тематики',
    preliminary_topic_mismatch: 'Проверен и отклонён: не соответствует теме', off_topic_group: 'Группа не соответствует теме',
    assessment_unavailable: 'AI-оценка временно недоступна', probation_full: 'Все места испытания заняты', probation_full_verified: 'Группа проверена; все места испытания заняты', projection_conflict: 'Конфликт подключения',
    shadow_preliminary_pass: 'Прошёл предварительную проверку',
    observed_useful: 'Полезность подтверждена', observed_useful_curation: 'Одобрен как куратор качественных публикаций', insufficient_evidence: 'Недостаточно данных для решения',
    observed_irrelevant: 'Недостаточно публикаций по теме', quality_drift: 'Качество снизилось',
    observed_ad_or_duplicates: 'Слишком много рекламы или дублей'
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
    const display = url ? host + (pathname === '/' ? '' : pathname) : source;
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
        href: url, target: '_blank', rel: 'noopener noreferrer', textContent: identity.display,
        title: 'Открыть источник в новой вкладке'
    }) : el('span', { textContent: identity.display });
    const reason = autopilotSourceReasonLabels[item.reason] || item.reason || 'Пока не проверен';
    const personalBlogText = { yes: 'Персональный блог', no: 'Не персональный блог', unknown: 'Тип блога пока не определён' }[item.personal_blog] || 'Тип блога пока не определён';
    const checked = item.checked_at ? new Date(item.checked_at * 1000).toLocaleString('ru-RU') : 'ещё не проверялся';
    const next = item.next_check_at ? new Date(item.next_check_at * 1000).toLocaleString('ru-RU') : '';
    return el('article', { className: 'autopilot-source-card' },
        icon,
        el('div', { className: 'autopilot-source-card-body' },
            el('div', { className: 'autopilot-source-card-top' },
                el('span', { className: 'autopilot-source-platform', textContent: platform.label }),
                el('span', { className: 'autopilot-source-state', textContent: autopilotSourceStateLabels[item.state] || item.state || 'Источник' })),
            el('div', { className: 'autopilot-source-card-name' }, name),
            el('div', { className: 'autopilot-source-card-reason', textContent: reason + ' · ' + personalBlogText }),
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
    const loadAllSources = async () => {
        const current = ++version;
        reload.disabled = true;
        status.textContent = 'Загрузка…';
        // Keep the current cards visible during refresh, including on errors.
        if (!list.children.length) list.append(el('p', { className: 'autopilot-source-empty', textContent: 'Загружаю источники…' }));
        try {
            let after = '', snapshotAvailable = true;
            const sources = [];
            const seen = new Set();
            // Read every page automatically, never ask the user to page through
            // a modest queue. Bound loops in case a buggy server repeats a cursor.
            for (let page = 0; page < 100; page++) {
                const result = await api('source_autopilot_sources', {
                    params: { topic: topic.topic, mode: topic.mode, state, after, limit: 500 }
                });
                if (current !== version || !dialog.open) return;
                if (state === 'found' && result.snapshot_available === false) {
                    snapshotAvailable = false;
                    break;
                }
                for (const item of result.sources || []) {
                    if (!seen.has(item.source_id)) {
                        seen.add(item.source_id);
                        sources.push(item);
                    }
                }
                const next = result.next_after || '';
                if (!next || next === after) break;
                after = next;
            }
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
    const container = $('autopilot-topics');
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
            return metric;
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
                el('span', { textContent: 'Причины ожидания: ' + Object.entries(topic.candidate_reasons || {})
                    .sort((a, b) => Number(b[1]) - Number(a[1]))
                    .map(([reason, count]) => (({
                        not_checked: 'ещё не проверены',
                        insufficient_fresh_samples: 'мало свежих публикаций',
                        feed_unavailable: 'лента недоступна',
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
                const result = await api('source_autopilot_demand', { body: { topic: topic.topic, mode: modeSelect.value, posts_per_day: topic.posts_per_day, horizon_days: topic.horizon_days } });
                renderAutopilot(result);
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
                renderAutopilot(await api('source_personal_blog_mode', {
                    body: { topic: topic.topic, mode: blogSelect.value }
                }));
            } catch (error) {
                $('autopilot-status').textContent = 'Не удалось сохранить фильтр блогов: ' + error.message;
                blogSelect.value = topic.personal_blog_mode || 'any';
                blogSelect.disabled = false;
            }
        });
        const blogFilter = el('label', { title: 'Фильтр применяется к выдаче постов и сборщику. Неизвестный тип исключается при строгом режиме.' },
            el('span', { textContent: 'Личные блоги: ' }), blogSelect);
        const history = el('details', {}, el('summary', { textContent: 'История решений' }));
        history.hidden = topic.mode === 'off';
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
                    const reasons = { discovered: 'найден', shadow_preliminary_pass: 'прошёл предварительную проверку', observed_useful: 'подтверждена полезность', observed_ad_or_duplicates: 'слишком много рекламы или дублей', observed_irrelevant: 'недостаточно материалов по теме', quality_drift: 'качество снизилось' };
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
        const request = el('button', { className: 'secondary-button', type: 'button', textContent: 'Запросить проверку', disabled: topic.mode === 'off' || topic.paused });
        request.addEventListener('click', async () => {
            request.disabled = true;
            try {
                renderAutopilot(await api('source_autopilot_request', { body: { topic: topic.topic } }));
                $('autopilot-status').textContent = 'Проверка запрошена. Срок повтора и суточные лимиты сохранены.';
            } catch (error) { $('autopilot-status').textContent = `Не удалось запросить проверку: ${error.message}`; request.disabled = false; }
        });
        const pause = el('button', { className: 'secondary-button', type: 'button', textContent: topic.paused ? 'Продолжить' : 'Пауза', disabled: topic.mode === 'off' });
        pause.addEventListener('click', async () => {
            pause.disabled = true;
            try { renderAutopilot(await api('source_autopilot_pause', { body: { topic: topic.topic, paused: !topic.paused } })); }
            catch (error) { $('autopilot-status').textContent = `Не удалось изменить паузу: ${error.message}`; pause.disabled = false; }
        });
        container.append(el('div', { className: 'autopilot-topic-row' }, description,
            el('div', { className: 'autopilot-topic-actions' }, modeSelect, blogFilter, pause, request), history));
    }
}

async function loadAutopilotTopicOptions() {
    const select = $('autopilot-topic');
    const current = select.value;
    const data = await api('topics');
    const topics = data.topics || [];
    const options = [el('option', { value: '', textContent: 'Выберите тему из дерева' })];
    const unique = new Set();
    for (const item of topics) {
        const title = String(item.title || '').trim();
        if (!title || unique.has(title.toLocaleLowerCase('ru-RU'))) continue;
        unique.add(title.toLocaleLowerCase('ru-RU'));
        options.push(el('option', {
            value: title,
            textContent: item.path && item.path !== title ? item.path : title
        }));
    }
    select.replaceChildren(...options);
    if (topics.some(item => item.title === current)) select.value = current;
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
            queue_ready: topic.queue_ready, queue_delayed: topic.queue_delayed,
            queue_unchecked: topic.queue_unchecked, checked_24h: topic.checked_24h,
            assessment_unavailable: topic.assessment_unavailable,
            decisions_24h: topic.decisions_24h, promoted_24h: topic.promoted_24h,
            candidate_reasons: topic.candidate_reasons,
            candidate_assessment_causes: topic.candidate_assessment_causes,
            probation_reasons: topic.probation_reasons,
            fetch_remaining: topic.fetch_remaining, model_remaining: topic.model_remaining,
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
async function autopilotExportSourcesForTopic(topic) {
    const sources = new Map();
    for (const state of autopilotExportStates) {
        let after = '';
        const cursors = new Set();
        for (let page = 0; page < 100; page++) {
            const data = await api('source_autopilot_sources', {
                params: { topic: topic.topic, mode: topic.mode, state, after, limit: 500 }
            });
            for (const item of data.sources || []) {
                if (!item.source_id) continue;
                // Re-checking may move a source to another state mid-export.
                // Keep only one row per ID, while preserving the reason shown.
                sources.set(item.source_id, {
                    source_id: autopilotExportSafeValue(item.source_id),
                    url: autopilotExportSafeValue(item.url),
                    state: item.state,
                    reason: item.reason || '',
                    reason_label: autopilotSourceReasonLabels[item.reason] || item.reason || '',
                    personal_blog: item.personal_blog || 'unknown',
                    first_seen_at: item.first_seen_at || 0,
                    checked_at: item.checked_at || 0,
                    next_check_at: item.next_check_at || 0
                });
            }
            const next = String(data.next_after || '');
            if (!next) break;
            if (next === after || cursors.has(next) || page === 99) {
                throw new Error('Сервер вернул некорректную или слишком длинную пагинацию (' + state + '). Список не скопирован частично.');
            }
            cursors.add(next);
            after = next;
        }
    }
    const entries = [...sources.values()];
    const counts = Object.fromEntries(autopilotExportStates.map(state => [
        state, entries.filter(item => item.state === state).length
    ]));
    return { topic: topic.topic, mode: topic.mode, count: entries.length, counts, sources: entries };
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
        $('autopilot-status').textContent = 'Скопировано ' + total.toLocaleString('ru-RU') +
            ' источников из ' + exported.length + ' тем. Вставь список в чат для проверки отклонений.';
    } catch (error) {
        $('autopilot-status').textContent = 'Не удалось скопировать список источников: ' + error.message;
    } finally {
        button.textContent = original;
        button.disabled = false;
    }
}

async function loadAutopilot() {
    if (DEBUG_MODE) { $('autopilot-status').textContent = 'В демо-режиме проверка недоступна.'; return; }
    try {
        await loadAutopilotTopicOptions();
        renderAutopilot(await api('source_autopilot_status'));
    } catch (error) { $('autopilot-status').textContent = `Сведения недоступны: ${error.message}`; }
}

async function saveAutopilot(event) {
    event.preventDefault();
    const button = event.currentTarget.querySelector('button');
    button.disabled = true;
    try {
        const selected = $('autopilot-topic');
        if (!selected.value || !selected.selectedOptions[0] || selected.selectedOptions[0].value !== selected.value) {
            throw new Error('Выберите тему из дерева.');
        }
        const result = await api('source_autopilot_demand', { body: { topic: selected.value, mode: $('autopilot-mode').value, posts_per_day: Number($('autopilot-rate').value), horizon_days: Number($('autopilot-horizon').value) } });
        renderAutopilot(result);
    } catch (error) { $('autopilot-status').textContent = `Настройки не сохранены: ${error.message}`; }
    finally { button.disabled = false; }
}

// ---- Источники темы ----

function avatar(source) {
    const box = el('span', { className: 'source-avatar', textContent: (source.name || source.id || '?').replace(/^[^\p{L}\p{N}]+/u, '').charAt(0).toUpperCase() || '?' });
    box.setAttribute('aria-hidden', 'true');
    if (source.avatar) {   // без lazy: картинка вне документа «лениво» не грузится
        const image = el('img', { src: source.avatar, alt: '', decoding: 'async', referrerPolicy: 'no-referrer' });
        image.addEventListener('load', () => box.replaceChildren(image), { once: true });
    }
    return box;
}

/** Карточка поста поверх карточки источника: лёгкая подложка, карточка смещена, источник виден под ней. */
let sourcePreview = null;

function closeSourcePreview() {
    if (!sourcePreview) return;
    document.removeEventListener('keydown', sourcePreview.onKey);
    window.removeEventListener('resize', sourcePreview.place);
    sourcePreview.overlay.remove();
    sourcePreview = null;
}

function openSourcePreview(source, index, anchorOf) {
    closeSourcePreview();
    const overlay = el('div', { className: 'source-preview-overlay', onclick: e => { if (e.target === overlay) closeSourcePreview(); } });
    const shown = { index: 0 };
    const counter = el('span', { className: 'source-preview-counter' });
    const img = el('img', { alt: source.name, loading: 'lazy', decoding: 'async', referrerPolicy: 'no-referrer', onload: () => place() });
    const step = delta => setIndex(shown.index + delta);
    const setIndex = i => {
        shown.index = (i + source.preview.length) % source.preview.length;
        img.src = source.preview[shown.index];
        counter.textContent = source.preview.length > 1 ? `${shown.index + 1} из ${source.preview.length}` : '';
    };
    const card = el('div', { className: 'source-preview-card', role: 'dialog', ariaLabel: `Пост источника «${source.name}»` },
        el('button', { type: 'button', className: 'source-preview-close', textContent: '✕', ariaLabel: 'Закрыть', onclick: closeSourcePreview }),
        img,
        el('div', { className: 'source-preview-foot' },
            source.preview.length > 1 ? el('button', { type: 'button', className: 'source-preview-nav', textContent: '←', ariaLabel: 'Предыдущий', onclick: () => step(-1) }) : null,
            el('span', { className: 'source-preview-name', textContent: source.name }),
            el('a', { href: source.url, target: '_blank', rel: 'noopener noreferrer', textContent: 'Открыть пост ↗' }),
            counter,
            source.preview.length > 1 ? el('button', { type: 'button', className: 'source-preview-nav', textContent: '→', ariaLabel: 'Следующий', onclick: () => step(1) }) : null));
    overlay.append(card);
    document.body.append(overlay);
    // Положение: от правого-нижнего угла карточки источника; на узком экране — по всей ширине
    const place = () => {
        const rect = anchorOf().getBoundingClientRect();
        const narrow = innerWidth <= 600;
        const width = narrow ? innerWidth - 24 : Math.min(rect.width * 0.85, 420);
        const left = Math.max(8, Math.min(narrow ? 12 : rect.left + 24, innerWidth - width - 8));
        const top = Math.max(8, Math.min(narrow ? rect.top + 12 : rect.top + 24, innerHeight - card.offsetHeight - 8));
        Object.assign(card.style, { width: `${width}px`, left: `${left}px`, top: `${top}px` });
    };
    const onKey = e => { if (e.key === 'Escape') closeSourcePreview(); };
    sourcePreview = { overlay, onKey, place };
    document.addEventListener('keydown', onKey);
    window.addEventListener('resize', place);
    setIndex(index);
    place();
}

// Названия старых RSS-источников уточняются по мере появления карточек на экране.
const topicTitles = { observer: null, queue: [], active: 0, generation: 0 };
function queueTopicTitle(card, source, generation) {
    topicTitles.queue.push({ card, source, generation });
    while (topicTitles.active < 2 && topicTitles.queue.length) {
        const job = topicTitles.queue.shift();
        if (job.generation !== topicTitles.generation || !job.card.isConnected) continue;
        topicTitles.active++;
        let timer;
        Promise.race([
            api('rss_source_title', { body: { source_id: job.source.source_id } }),
            new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('Источнику нужно больше времени')), 35000); })
        ]).then(data => {
            if (job.generation !== topicTitles.generation || !job.card.isConnected || !data.name) return;
            job.source.name = data.name;
            job.source.needs_title = false;
            const node = job.card.querySelector('.source-head-text strong');
            if (node) { node.textContent = data.name; node.title = data.name; }
        }).catch(() => {}).finally(() => { clearTimeout(timer); topicTitles.active--; drainTopicTitles(); });
    }
}
function drainTopicTitles() {
    if (!topicTitles.queue.length || topicTitles.active >= 2) return;
    const job = topicTitles.queue.shift();
    queueTopicTitle(job.card, job.source, job.generation);
}

function topicSourceCard(source) {
    const cover = () => el('div', { className: 'source-cover' }, avatar(source));
    const slides = source.preview.map(src => ({ kind: 'image', src, poster: src, sourceUrl: source.url, alt: source.name }));
    let media = cover();
    if (slides.length) {
        media = el('div', { className: `proposal-media mosaic-${slides.length}` });
        slides.forEach((slide, i) => {
            const tile = el('button', { className: 'proposal-tile is-loading', type: 'button', title: 'Открыть пост', onclick: e => { e.stopPropagation(); openSourcePreview(source, i, () => card); } },
                el('img', { src: slide.src, alt: '', loading: 'lazy', decoding: 'async', referrerPolicy: 'no-referrer',
                    onload: () => tile.classList.remove('is-loading'),
                    onerror: () => { tile.remove(); const left = media.children.length; left ? media.className = `proposal-media mosaic-${left}` : media.replaceWith(cover()); } }));
            media.append(tile);
        });
    }
    const chips = [source.subscribers > 0 && chip('подписчики', COUNT.format(source.subscribers)), source.view_rate != null && chip('просмотры', `${Math.round(source.view_rate * 100)}%`, 'Какая доля подписчиков видит пост'),
        source.score != null && chip('оценка', `${source.score}%`, 'Оценка автоподбора: по теме, аудитория, доля просмотров')].filter(Boolean);
    const stateLabels = { candidate: 'кандидат', probation: 'на испытании', active: 'подтверждён', rejected: 'отклонён', cooldown: 'ожидает проверки' };
    const healthLabels = { rate_limited: 'площадка ограничила запросы', feed_unavailable: 'лента временно недоступна', source_not_found: 'лента не найдена', assessment_unavailable: 'оценка временно недоступна', insufficient_evidence: 'мало наблюдений', probation_full: 'места на испытании заняты' };
    const evidence = source.autopilot?.evidence;
    const observation = evidence?.evaluated ? `оценено постов: ${evidence.evaluated}, по теме: ${evidence.relevant ?? 0}, проходов: ${evidence.successful_passes ?? 0}` : '';
    const notes = [!source.own && (source.topics.length ? `из общей темы: ${source.topics.join(', ')}` : 'источник для всех тем'), source.auto && 'найден автоподбором', stateLabels[source.autopilot?.state], healthLabels[source.autopilot?.reason], observation].filter(Boolean);
    const card = el('article', { className: `proposal-card source-tile-card${source.frozen ? ' is-frozen' : ''}` }, media,
        el('div', { className: 'source-head' }, avatar(source), el('div', { className: 'source-head-text' }, el('strong', { textContent: source.name, title: source.name }),
            el('a', { href: source.url, target: '_blank', rel: 'noopener noreferrer', textContent: source.handle })),
            el('span', { className: 'source-platform-tag', textContent: source.platform_label || source.kind || { 'ВКонтакте': 'VK', 'Одноклассники': 'ОК', 'TikTok': 'TikTok' }[source.platform] || source.platform })),
        chips.length ? el('div', { className: 'source-chips' }, ...chips) : null,
        el('span', { className: `source-status${source.frozen ? ' is-frozen' : ''}`, textContent: ['Двач', '4chan'].includes(source.platform) ? 'тред имиджборда'
            : source.frozen ? `заморожен: ${source.frozen_reason || 'не активен'}` : source.activity || 'активен' }),
        notes.length ? el('p', { className: 'source-note', textContent: notes.join(' · ') }) : null);
    const actions = el('div', { className: 'proposal-actions' }, el('a', { className: 'proposal-action', href: source.url, target: '_blank', rel: 'noopener noreferrer', textContent: 'Открыть ↗' }));
    if (!DEBUG_MODE && ['RSS', 'Telegram', 'ВКонтакте', 'Одноклассники', 'TikTok'].includes(source.platform)) actions.append(el('button', { className: 'proposal-action', type: 'button', textContent: source.pinned ? 'Открепить' : 'Закрепить для меня', onclick: async () => {
        try {
            await api('source_personal_pin', { body: { source_id: source.source_id, pinned: !source.pinned } });
            source.pinned = !source.pinned;
            if (!source.pinned) await loadTopicSources();
            else renderTopicSources();
        } catch (error) { log(`Не удалось изменить закрепление: ${error.message}`); }
    } }));
    if (source.muted) {   // выключен по оценкам пользователя (TOPICS_SEARCH_PROMPT.md, часть F)
        card.classList.add('is-frozen');
        card.querySelector('.source-status')?.replaceChildren(document.createTextNode('выключен по вашим оценкам'));
        actions.append(el('button', { className: 'proposal-action', type: 'button', textContent: 'Включить', onclick: async () => {
            try {
                await api('source_mute', { body: { source_id: source.source_id, on: true } });
                source.muted = false;
                log(`Источник «${source.name}» снова в подборе этой темы.`);
                renderTopicSources();
            } catch (error) { log(`Не удалось включить источник: ${error.message}`); }
        } }));
    } else if (!DEBUG_MODE) {
        actions.append(el('button', { className: 'proposal-action', type: 'button', textContent: 'Исключить для меня', onclick: async () => {
            try {
                await api('source_mute', { body: { source_id: source.source_id, on: false } });
                source.muted = true;
                renderTopicSources();
            } catch (error) { log(`Не удалось исключить источник: ${error.message}`); }
        } }));
    }
    if (!DEBUG_MODE && source.own && !source.autopilot_managed) actions.append(el('button', { className: 'proposal-action proposal-hide', type: 'button', textContent: '✕ Убрать', title: `Убрать из темы «${topicSources.topic}»`, onclick: async () => {
        if (!(await ask('Посты из него перестанут попадать в подбор по этой теме.', { title: `Убрать «${source.name}» из темы «${topicSources.topic}»?`, ok: 'Убрать', danger: true }))) return;
        card.classList.add('is-removing');
        try {
            await api('remove_topic_source', { body: { platform: source.platform, id: source.id, name: source.name } });
            topicSources.sources = topicSources.sources.filter(o => o !== source);
            log(`Источник «${source.name}» убран из темы «${topicSources.topic}».`);
            renderTopicSources();
        } catch (error) { card.classList.remove('is-removing'); log(`Не удалось убрать источник: ${error.message}`); }
    } }));
    card.append(actions);
    if (source.needs_title) {
        card._titleSource = source;
        if (topicTitles.observer) topicTitles.observer.observe(card);
        else requestAnimationFrame(() => queueTopicTitle(card, source, topicTitles.generation));
    }
    return card;
}

function renderTopicSources() {
    const { sources, topic, filter } = topicSources;
    const visible = sources.filter(s => filter === 'all' || s.platform === filter);
    $('topic-sources-title').textContent = topic ? `Источники темы «${topic}»` : 'Источники темы';
    $('topic-sources-count').textContent = sources.length ? `${visible.length} из ${sources.length}` : '';
    $('topic-sources-status').textContent = visible.length ? '' : sources.length ? 'На этой площадке источников темы нет.' : 'У темы пока нет источников: нажмите «Подобрать источники» или одобряйте предложенные выше.';
    const generation = ++topicTitles.generation;
    topicTitles.observer?.disconnect();
    topicTitles.queue = [];
    topicTitles.observer = 'IntersectionObserver' in window ? new IntersectionObserver(entries => {
        for (const entry of entries) if (entry.isIntersecting && entry.target._titleSource) {
            topicTitles.observer?.unobserve(entry.target);
            queueTopicTitle(entry.target, entry.target._titleSource, generation);
        }
    }, { rootMargin: '250px' }) : null;
    $('topic-sources-grid').replaceChildren(...visible.map(topicSourceCard));
    document.querySelectorAll('[data-topic-source-filter]').forEach(b => b.classList.toggle('active', b.dataset.topicSourceFilter === filter));
}

async function loadTopicSources() {
    if (!$('topic-sources-section').open || topicSources.loading) return;
    topicSources.loading = true;
    try {
        const data = await api('topic_sources');
        Object.assign(topicSources, { sources: data.sources || [], topic: data.topic || '' });
        renderTopicSources();
    } catch (error) { $('topic-sources-status').textContent = `Не удалось загрузить источники: ${error.message}`; }
    finally { topicSources.loading = false; }
}

/** «Добавить источник»: ссылка → проверка на сервере → источник темы; список обновляется. */
async function addSource(event) {
    event.preventDefault();
    const [input, button, status] = [$('source-add-link'), $('source-add-button'), $('source-add-status')];
    const link = input.value.trim();
    if (!link) return;
    button.disabled = true;
    status.textContent = 'Проверяю источник…';
    try {
        const result = await api('add_topic_source', { body: { link } });
        status.textContent = result.added ? `Добавлен: ${result.name} (свежих постов с картинками: ${result.posts})` : `Уже в теме: ${result.name}`;
        input.value = '';
        await loadTopicSources();
    } catch (error) { status.textContent = error.message; }
    finally { button.disabled = false; }
}

// ---- Свой контент: картинки и CSV сразу в подборку ----

/** Отправка файлов; итог — строка статуса и ошибки списком (textContent). */
async function sendOwn(action, form, button) {
    const [status, errors] = [$('own-content-status'), $('own-content-errors')];
    button.disabled = true;
    status.textContent = 'Загружаю…';
    errors.replaceChildren();
    try {
        const result = await api(action, { body: form });
        status.textContent = `Добавлено в подборку: ${result.added}` + (result.skipped ? `, пропущено: ${result.skipped}` : '');
        errors.replaceChildren(...(result.errors || []).map(text => el('li', { textContent: text })));
        if (result.added) { log(`Свой контент: добавлено в подборку — ${result.added}.`); loadCurrentItem(); }
    } catch (error) { status.textContent = error.message; }
    finally { button.disabled = false; }
}

/** Образец CSV собирается на клиенте. */
function downloadCsvSample(event) {
    event.preventDefault();
    const text = '\uFEFFtext;image\r\nПодпись к первому посту;https://example.com/photo1.jpg\r\nВторой пост с двумя картинками;https://example.com/photo2.jpg|https://example.com/photo3.jpg\r\n';
    const link = el('a', { href: URL.createObjectURL(new Blob([text], { type: 'text/csv;charset=utf-8' })), download: 'posta-example.csv' });
    link.click();
    URL.revokeObjectURL(link.href);
}

function initializeOwnContent() {
    const [files, csv] = [$('own-files'), $('own-csv')];
    $('own-upload-button').addEventListener('click', () => files.click());
    $('own-csv-button').addEventListener('click', () => csv.click());
    $('own-csv-sample').addEventListener('click', downloadCsvSample);
    files.addEventListener('change', async () => {
        if (!files.files.length) return;
        const selected = [...files.files];
        $('own-upload-button').disabled = true;
        $('own-content-status').textContent = 'Подготавливаю картинки на вашем устройстве…';
        const form = new FormData();
        for (const file of selected) form.append('files[]', await prepareOwnImage(file));
        form.append('text', $('own-text').value.trim());
        form.append('album', $('own-album').checked ? '1' : '0');
        sendOwn('own_upload', form, $('own-upload-button')).finally(() => { files.value = ''; });
    });
    csv.addEventListener('change', () => {
        if (!csv.files.length) return;
        const form = new FormData();
        form.append('csv', csv.files[0]);
        sendOwn('own_import_csv', form, $('own-csv-button')).finally(() => { csv.value = ''; });
    });
}

// ---- Лучшие посты источника: поиск по ссылке, выбор и забор в подборку ----

const BEST_POSTS_TAKE_MAX = 20;   // как на сервере
const BEST_SORT_LABELS = { date: 'по дате', likes: 'по лайкам', reposts: 'по репостам', comments: 'по комментариям', views: 'по просмотрам', viral: 'по виральности' };
const bestPosts = { key: '', posts: [], selected: new Set() };

function updateBestPostsButton() {
    const take = $('best-posts-take');
    take.textContent = `Забрать в подборку (${bestPosts.selected.size})`;
    take.disabled = bestPosts.selected.size === 0;
}

/** Отметка поста; сверх лимита за раз отметка снимается. */
function toggleBestPost(token, check) {
    if (check.checked && bestPosts.selected.size >= BEST_POSTS_TAKE_MAX) {
        check.checked = false;
        $('best-posts-status').textContent = `За раз — не больше ${BEST_POSTS_TAKE_MAX} постов.`;
        return;
    }
    check.checked ? bestPosts.selected.add(token) : bestPosts.selected.delete(token);
    updateBestPostsButton();
}

function bestPostCard(post) {
    const check = el('input', { type: 'checkbox', checked: bestPosts.selected.has(post.token) });
    check.addEventListener('change', () => toggleBestPost(post.token, check));
    const stats = [post.likes != null && `♥ ${COUNT.format(post.likes)}`, post.comments != null && `💬 ${COUNT.format(post.comments)}`,
        post.reposts != null && `↪ ${COUNT.format(post.reposts)}`, post.views != null && `👁 ${COUNT.format(post.views)}`, post.viral > 0 && `виральность ${post.viral}`];
    const date = post.posted_at ? new Date(post.posted_at * 1000).toLocaleDateString('ru-RU') : '';
    const link = safeSourcePostUrl({ source_url: post.source_url, source_key: post.source_key });
    return el('label', { className: 'proposal-card best-post' },
        el('img', { className: 'best-post-image', src: post.image, alt: '', loading: 'lazy', decoding: 'async', referrerPolicy: 'no-referrer' }),
        el('span', { className: 'best-post-meta' }, check, el('span', { textContent: [date, ...stats].filter(Boolean).join(' · ') })),
        el('p', { className: 'best-post-text', textContent: post.text }),
        link && el('a', { href: link, target: '_blank', rel: 'noopener noreferrer', textContent: 'Открыть пост ↗' }));
}

function renderBestPosts() {
    $('best-posts-grid').replaceChildren(...bestPosts.posts.map(bestPostCard));
    $('best-posts-actions').hidden = bestPosts.posts.length === 0;
    updateBestPostsButton();
}

/** Сортировки — только те, что умеет площадка источника. */
function fillBestSorts(sorts, current) {
    const select = $('best-posts-sort');
    select.replaceChildren(...sorts.map(s => el('option', { value: s, textContent: BEST_SORT_LABELS[s] || s })));
    select.value = sorts.includes(current) ? current : 'date';
}

async function searchBestPosts(event) {
    event?.preventDefault();
    const [link, days, sort, type, phrase] = [$('best-posts-link').value.trim(), $('best-posts-days').value, $('best-posts-sort').value, $('best-posts-type').value, $('best-posts-phrase').value.trim()];
    if (!link) return;
    const [button, status] = [$('best-posts-button'), $('best-posts-status')];
    button.disabled = true;
    status.textContent = 'Ищу лучшие посты…';
    try {
        const data = await api('best_posts', { params: { link, days, sort, type, phrase } });
        Object.assign(bestPosts, { key: data.key, posts: data.posts, selected: new Set() });
        fillBestSorts(data.sorts, data.sort);
        status.textContent = data.posts.length ? `${data.source.name}: постов — ${data.posts.length}` : 'За этот период подходящих постов нет.';
    } catch (error) {
        Object.assign(bestPosts, { posts: [], selected: new Set() });
        status.textContent = error.message;
    } finally {
        button.disabled = false;
        renderBestPosts();
    }
}

function selectAllBestPosts() {
    bestPosts.selected = new Set(bestPosts.posts.slice(0, BEST_POSTS_TAKE_MAX).map(p => p.token));
    renderBestPosts();
}

/** Выбранные посты — в подборку; повторы и нескачанные картинки пропускаются. */
async function takeBestPosts() {
    const [button, status] = [$('best-posts-take'), $('best-posts-status')];
    button.disabled = true;
    status.textContent = 'Забираю в подборку…';
    const picked = bestPosts.selected.size;
    try {
        const result = await api('best_posts_take', { body: { key: bestPosts.key, tokens: [...bestPosts.selected] } });
        status.textContent = `Добавлено в подборку: ${result.added}` + (result.added < picked ? ' (часть уже была в подборке или не скачалась)' : '');
        bestPosts.selected = new Set();
        renderBestPosts();
        if (result.added) { log(`Лучшие посты: в подборку — ${result.added}.`); loadCurrentItem(); }
    } catch (error) { status.textContent = error.message; }
    finally { updateBestPostsButton(); }
}

function initializeBestPosts() {
    $('best-posts-form').addEventListener('submit', searchBestPosts);
    $('best-posts-sort').addEventListener('change', () => { if ($('best-posts-link').value.trim()) searchBestPosts(); });
    $('best-posts-select-all').addEventListener('click', selectAllBestPosts);
    $('best-posts-take').addEventListener('click', takeBestPosts);
}

// ---- Свой контент: папка и Яндекс Диск ----

const OWN_FOLDER_LIMIT = 1000;            // файлов за один выбор папки (и в сутки на сервере)
const OWN_FILE_MAX = 2 * 1024 * 1024;     // upload_max_filesize на сервере
const OWN_IMAGE_EXT = /\.(jpe?g|png|gif|webp|bmp)$/i;
const OWN_VIDEO_EXT = /\.(mp4|mov|webm|avi|mkv|m4v)$/i;
let ownFolderBusy = false;
let ownFolderStop = false;

/** Файлы папки по одному (лимит загрузки сервера); подпись — из .txt с тем же именем. */
async function importOwnFolder(files) {
    if (ownFolderBusy) return;
    ownFolderBusy = true;
    const [status, errors, stop, button] = [$('own-content-status'), $('own-content-errors'), $('own-folder-stop'), $('own-folder-button')];
    const stem = file => file.webkitRelativePath.replace(/\.[^./]+$/, '');
    const media = files.filter(f => OWN_IMAGE_EXT.test(f.name) || OWN_VIDEO_EXT.test(f.name));
    const captions = new Map(files.filter(f => /\.txt$/i.test(f.name)).map(f => [stem(f), f]));
    const queue = media.slice(0, OWN_FOLDER_LIMIT);
    errors.replaceChildren();
    ownFolderStop = false;
    button.disabled = true;
    stop.hidden = false;
    let added = 0;
    try {
        for (let i = 0; i < queue.length && !ownFolderStop; i++) {
            const original = queue[i];
            const file = await prepareOwnImage(original);
            status.textContent = `Загружено ${i} из ${queue.length}` + (added ? `, в подборку: ${added}` : '');
            if (file.size > OWN_FILE_MAX) {
                errors.append(el('li', { textContent: `${original.name}: файл больше 2 МБ после подготовки.` }));
                continue;
            }
            const note = captions.has(stem(original)) ? (await captions.get(stem(original)).text()).trim() : $('own-text').value.trim();
            const form = new FormData();
            form.append('files[]', file);
            form.append('text', note.slice(0, 1000));
            form.append('album', '0');
            try {
                const result = await api('own_upload', { body: form });
                added += result.added;
                errors.append(...(result.errors || []).map(text => el('li', { textContent: text })));
            } catch (error) { errors.append(el('li', { textContent: `${file.name}: ${error.message}` })); }
        }
        const stopped = ownFolderStop ? ' Остановлено.' : '';
        const rest = media.length > OWN_FOLDER_LIMIT ? ` Загружены первые ${OWN_FOLDER_LIMIT} файлов.` : '';
        status.textContent = `Добавлено в подборку: ${added} из ${queue.length}.${stopped}${rest}`;
        if (added) { log(`Свой контент: из папки в подборку — ${added}.`); loadCurrentItem(); }
    } finally {
        ownFolderBusy = false;
        ownFolderStop = false;
        stop.hidden = true;
        button.disabled = false;
    }
}

/** Импорт картинок с Яндекс Диска: до 100 за нажатие; если остались — повторное нажатие продолжает. */
async function importYadisk() {
    const [input, button, status, errors] = [$('own-yadisk-url'), $('own-yadisk-button'), $('own-content-status'), $('own-content-errors')];
    const url = input.value.trim();
    if (!url) return;
    button.disabled = true;
    status.textContent = 'Импортирую с Яндекс Диска…';
    try {
        const result = await api('own_import_yadisk', { body: { url } });
        status.textContent = `Добавлено в подборку: ${result.added} (проверено ${result.processed} из ${result.total})` + (result.skipped ? `, видео пропущено: ${result.skipped}` : '')
            + (result.done ? '. Папка готова.' : '. Нажмите ещё раз — продолжу с того места.');
        errors.replaceChildren(...(result.errors || []).map(text => el('li', { textContent: text })));
        if (result.added) { log(`Свой контент: с Яндекс Диска в подборку — ${result.added}.`); loadCurrentItem(); }
    } catch (error) { status.textContent = error.message; }
    finally { button.disabled = false; }
}

/** Твиты X по ссылкам (до 20 за нажатие, до 100 в сутки). */
async function importX() {
    const [input, button, status, errors] = [$('own-x-links'), $('own-x-button'), $('own-content-status'), $('own-content-errors')];
    const links = input.value.split('\n').map(s => s.trim()).filter(Boolean);
    if (!links.length) return;
    button.disabled = true;
    status.textContent = 'Загружаю твиты…';
    try {
        const result = await api('own_import_x', { body: { links } });
        status.textContent = `Добавлено в подборку: ${result.added}` + (result.skipped ? `, пропущено: ${result.skipped}` : '');
        errors.replaceChildren(...(result.errors || []).map(([link, text]) => el('li', { textContent: `${link}: ${text}` })));
        if (result.added) { log(`Свой контент: твиты X в подборку — ${result.added}.`); input.value = ''; loadCurrentItem(); }
    } catch (error) { status.textContent = error.message; }
    finally { button.disabled = false; }
}

function initializeOwnFolder() {
    const folder = $('own-folder');
    $('own-folder-button').addEventListener('click', () => folder.click());
    $('own-folder-stop').addEventListener('click', () => { ownFolderStop = true; });
    folder.addEventListener('change', () => { const files = Array.from(folder.files); folder.value = ''; if (files.length) importOwnFolder(files); });
    $('own-yadisk-button').addEventListener('click', importYadisk);
    $('own-x-button').addEventListener('click', importX);
}

// ---- Зеркало источника: личный источник → подборка или очередь канала ----

const mirrorState = { channels: [] };

/** Правила из формы; замены — строки «что → на что», галочка «регулярное выражение» относится ко всем. */
function mirrorRulesFromForm() {
    const words = id => $(id).value.split(',').map(w => w.trim()).filter(Boolean);
    const regex = $('mirror-regex').checked;
    const replace = $('mirror-replace').value.split('\n').map(line => line.split(/\s*(?:→|->)\s*/))
        .filter(([from]) => from && from.trim()).map(([from, to = '']) => ({ from: from.trim(), to: to.trim(), regex }));
    return { include: words('mirror-include'), exclude: words('mirror-exclude'), replace,
        strip_links: $('mirror-strip-links').checked, strip_mentions: $('mirror-strip-mentions').checked, strip_hashtags: $('mirror-strip-hashtags').checked,
        append: $('mirror-append').value.trim() };
}

function mirrorRow(m) {
    const mode = m.mode === 'queue' ? `в очередь: ${m.channel_title || 'канал'}` : 'в подборку';
    const checked = m.last_checked_at ? `последняя проверка: ${new Date(m.last_checked_at * 1000).toLocaleString('ru-RU')}` : 'ещё не проверялось';
    return el('li', { className: 'mirror-row' },
        el('span', { className: 'mirror-name', textContent: m.name }),
        el('span', { className: 'best-post-meta', textContent: `${mode} · ${checked}` }),
        el('button', { type: 'button', className: 'mirror-delete', textContent: 'Удалить', onclick: () => deleteMirror(m.id) }));
}

async function loadMirrors() {
    try {
        const data = await api('mirror_list');
        $('mirror-list').replaceChildren(...data.mirrors.map(mirrorRow));
    } catch (error) { $('mirror-status').textContent = error.message; }
}

async function deleteMirror(id) {
    try {
        await api('mirror_delete', { body: { id } });
        await loadMirrors();
    } catch (error) { $('mirror-status').textContent = error.message; }
}

async function saveMirror(event) {
    event.preventDefault();
    const [button, status, mode] = [$('mirror-button'), $('mirror-status'), $('mirror-mode').value];
    button.disabled = true;
    status.textContent = 'Проверяю источник…';
    try {
        const result = await api('mirror_save', { body: { link: $('mirror-link').value.trim(), mode, channel: mode === 'queue' ? $('mirror-channel').value : '', rules: mirrorRulesFromForm() } });
        status.textContent = `Зеркало добавлено: ${result.mirror.name}`;
        $('mirror-link').value = '';
        await loadMirrors();
    } catch (error) { status.textContent = error.message; }
    finally { button.disabled = false; }
}

/** Каналы для очереди — из очереди Telegram; без подключённого Telegram список пуст. */
async function loadMirrorChannels() {
    try {
        const data = await api('telegram_queue');
        mirrorState.channels = data.connected ? data.channels || [] : [];
    } catch { mirrorState.channels = []; }
    $('mirror-channel').replaceChildren(...mirrorState.channels.map(c => el('option', { value: c.id, textContent: c.chat_title || c.id })));
    updateMirrorMode();
}

function updateMirrorMode() {
    const queue = $('mirror-mode').value === 'queue';
    $('mirror-channel').hidden = !queue;
    if (queue && !mirrorState.channels.length) $('mirror-status').textContent = 'Очередь канала — после подключения Telegram-канала.';
}

function initializeMirror() {
    $('mirror-form').addEventListener('submit', saveMirror);
    $('mirror-mode').addEventListener('change', updateMirrorMode);
}

export function initializeSourceTab() {
    const toolsDialog = $('source-tools-dialog');
    const openTools = () => { if (!toolsDialog.open) toolsDialog.showModal(); };
    $('source-tools-open').addEventListener('click', openTools);
    $('source-tools-close').addEventListener('click', () => toolsDialog.close());
    initializeOwnContent();
    initializeOwnFolder();
    initializeBestPosts();
    initializeMirror();
    $('source-add-form').addEventListener('submit', addSource);
    $('autopilot-form').addEventListener('submit', saveAutopilot);
    $('autopilot-copy-diagnostic').addEventListener('click', copyAutopilotDiagnostic);
    $('autopilot-copy-sources').addEventListener('click', copyAutopilotSources);
    window.setInterval(() => { if (isTabActive('source-tab')) loadAutopilot(); }, 30000);
    document.querySelectorAll('[data-topic-source-filter]').forEach(b => b.addEventListener('click', () => { topicSources.filter = b.dataset.topicSourceFilter; renderTopicSources(); }));
    $('topic-sources-refresh').addEventListener('click', loadTopicSources);
    initRegistry();
    $('topic-sources-section').addEventListener('toggle', () => { if ($('topic-sources-section').open) loadTopicSources(); });
    $('registry-section').addEventListener('toggle', () => { if ($('registry-section').open) loadRegistry(); });
    onTabOpen('source-tab', () => { loadAutopilot(); loadTopicSources(); if ($('registry-section').open) loadRegistry(); loadMirrors(); loadMirrorChannels(); });
    onParserStateChange(running => {
        if (!running && isTabActive('source-tab')) loadTopicSources();
    });
}
