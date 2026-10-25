// video-pending.js — ожидание ролика: опрос статуса раз в 3–4 с (пока качается — раз в секунду), не дольше 5 минут,
// не больше трёх опросов сразу на страницу. Проверка — функция check(): {state: 'pending'|'ready'|'failed', video?, error?, progress?}.
// Скрытая вкладка — опрос на паузе. videoProgress() — полоса загрузки поверх плитки, если ждать дольше двух секунд.
import { api, el } from './core.js';

const POLL_MS = 3500;
const POLL_ACTIVE_MS = 1000;   // ролик уже качается: полоса должна двигаться
const ACTIVE_STAGES = ['probe', 'download', 'merge', 'convert', 'telegram'];
const SHOW_AFTER_MS = 2000;    // быстрая загрузка — без полосы
const LIMIT_MS = 5 * 60 * 1000;
const MAX_AT_ONCE = 3;
let running = 0;
const queue = [];

/** Даёт слот опроса (не больше MAX_AT_ONCE на страницу). */
function slot() {
    return new Promise(resolve => {
        const go = () => { running++; resolve(); };
        if (running < MAX_AT_ONCE) go(); else queue.push(go);
    });
}

function release() {
    running--;
    queue.shift()?.();
}

/** Опрашивает check() до готовности или ошибки; возвращает {stop}. onReady(result), onFail(error), onProgress(progress|null). */
export function watchVideo(check, { onReady, onFail, onProgress } = {}) {
    let stopped = false;
    const started = Date.now();
    (async () => {
        while (!stopped && Date.now() - started < LIMIT_MS) {
            await slot();
            let result;
            try {
                result = document.hidden ? { state: 'pending' } : await check();
            } catch (error) {
                result = { state: 'failed', error: error.message };
            } finally {
                release();
            }
            if (stopped) return;
            if (result.state === 'ready') return onReady?.(result);
            if (result.state === 'failed') return onFail?.(result.error || 'Видео не скачалось.');
            if ('progress' in result) onProgress?.(result.progress);
            await new Promise(resolve => setTimeout(resolve, ACTIVE_STAGES.includes(result.progress?.stage) ? POLL_ACTIVE_MS : POLL_MS));
        }
        if (!stopped) onFail?.('Видео скачивается слишком долго: попробуйте позже.');
    })();
    return { stop() { stopped = true; } };
}

/** Проверка по адресу ролика (Telegram, TikTok, Shorts): состояние очереди. */
export function pageCheck(page) {
    return async () => {
        const s = await api('video_status', { params: { url: page } });
        if (s.status === 'done' && s.local) return { state: 'ready', video: s.local };
        if (s.status === 'failed') return { state: 'failed', error: s.error || 'Видео не скачалось.' };
        if (s.status === 'none') return { state: 'failed', error: 'Задачи скачивания нет: запустите загрузку заново.' };
        return { state: 'pending', progress: s.progress || null };
    };
}

/** Проверка по записи подборки: появился ли ролик в пуле (get_item_video). */
export function itemCheck(image) {
    return async () => {
        const r = await api('get_item_video', { params: { image } });
        if (r.video_unavailable) return { state: 'failed', error: 'Видео не скачалось и показано не будет.' };
        if (r.video) return { state: 'ready', video: r.display_video || r.video, video_local: r.video_local };
        return { state: 'pending', progress: r.progress || null };
    };
}

/**
 * Полоса загрузки ролика поверх плитки host (у host должен быть position: relative): стадия, процент и полосатая полоса,
 * как у поиска. Появляется, только если ждать дольше двух секунд. compact — для маленьких миниатюр: без подписи.
 * Возвращает {update(progress), stop()}; progress — {label, percent|null} с сервера.
 */
export function videoProgress(host, { compact = false, title = 'Видео загружается' } = {}) {
    const label = el('span', { className: 'vp-label', textContent: title });
    const percent = el('span', { className: 'vp-percent' });
    const fill = el('span', { className: 'vp-fill' });
    const root = el('div', { className: `vp is-indeterminate${compact ? ' is-compact' : ''}`, role: 'progressbar', ariaLabel: 'Загрузка видео', hidden: true },
        compact ? null : el('div', { className: 'vp-head' }, label, percent), el('div', { className: 'vp-track' }, fill));
    host.append(root);
    let shown = false;
    const count = delta => {   // полос на плитке может быть две (обложка и ролик): класс снимается, когда скрыта последняя
        const n = (Number(host.dataset.vp) || 0) + delta;
        host.dataset.vp = String(n);
        host.classList.toggle('has-vp', n > 0);
    };
    const timer = setTimeout(() => { root.hidden = false; shown = true; count(1); }, SHOW_AFTER_MS);
    return {
        update(p) {
            if (!p) return;
            label.textContent = p.label || title;
            const known = typeof p.percent === 'number';
            root.classList.toggle('is-indeterminate', !known);
            fill.style.width = known ? `${Math.max(3, p.percent)}%` : '';
            percent.textContent = known ? `${Math.round(p.percent)}%` : '';
            if (known) root.setAttribute('aria-valuenow', String(Math.round(p.percent))); else root.removeAttribute('aria-valuenow');
            root.title = known ? `${p.label}: ${Math.round(p.percent)}%` : p.label;
        },
        stop() { clearTimeout(timer); root.remove(); if (shown) { shown = false; count(-1); } },
    };
}

/**
 * Картинка с полосой загрузки на host, если грузится дольше двух секунд. Грузится, когда host показался на экране (как loading="lazy").
 * Своя (кэш сайта) — потоком, с процентом; чужая — обычной загрузкой, полоса без процента (чужой поток браузер читать не даёт).
 * Ошибка — как у обычной картинки (событие error на img).
 */
export function loadImage(img, src, host, { compact = false } = {}) {
    const start = () => {
        let bar = videoProgress(host, { compact, title: 'Картинка загружается' });
        const finish = () => { bar?.stop(); bar = null; };
        img.addEventListener('load', finish, { once: true });
        img.addEventListener('error', finish, { once: true });
        let own = false;
        try { own = new URL(src, location.href).origin === location.origin; } catch {}
        if (!own || !window.ReadableStream) { img.src = src; return; }
        (async () => {
            try {
                const response = await fetch(src);
                if (!response.ok || !response.body) throw new Error(String(response.status));
                const total = Number(response.headers.get('Content-Length')) || 0;
                const reader = response.body.getReader();
                const chunks = [];
                let done = 0;
                for (;;) {
                    const part = await reader.read();
                    if (part.done) break;
                    chunks.push(part.value);
                    done += part.value.length;
                    if (total) bar?.update({ label: 'Картинка загружается', percent: Math.min(99, done / total * 100) });
                }
                const url = URL.createObjectURL(new Blob(chunks, { type: response.headers.get('Content-Type') || '' }));
                img.addEventListener('load', () => URL.revokeObjectURL(url), { once: true });
                img.src = url;
            } catch {
                img.src = src;   // не вышло потоком — обычная загрузка (и обычная обработка ошибки)
            }
        })();
    };
    if (!window.IntersectionObserver) return start();
    const seen = new IntersectionObserver(entries => {
        if (!entries.some(e => e.isIntersecting)) return;
        seen.disconnect();
        start();
    }, { rootMargin: '200px' });
    seen.observe(host);
}
