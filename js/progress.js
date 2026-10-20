// progress.js — универсальная полоса прогресса: создаётся один раз, дальше обновляется через set().
import { el } from './core.js';

/**
 * Возвращает { root, set(done, total, label?) }. root вставляется в любое место страницы.
 * Если total = 0, полоса пустая. Подпись по умолчанию «done/total», её можно заменить через label.
 */
export function createProgress({ className = '' } = {}) {
    const fill = el('div', { className: 'progress-fill' });
    const text = el('span', { className: 'progress-text' });
    const root = el('div', { className: `progress ${className}`.trim() }, el('div', { className: 'progress-bar' }, fill), text);
    root.setAttribute('role', 'progressbar');
    return {
        root,
        /** Плавное заполнение в процентах (0–100), подпись «NN%». */
        percent(value) {
            root.classList.remove('is-busy');
            const p = Math.min(100, Math.max(0, value));
            fill.style.width = `${p.toFixed(1)}%`;
            text.textContent = `${Math.round(p)}%`;
            root.setAttribute('aria-valuemin', '0');
            root.setAttribute('aria-valuemax', '100');
            root.setAttribute('aria-valuenow', String(Math.round(p)));
        },
        /** Процесс идёт, но сколько осталось, неизвестно: полоса пульсирует. */
        busy(label) {
            root.classList.add('is-busy');
            fill.style.width = '100%';
            text.textContent = label;
            root.removeAttribute('aria-valuenow');
            root.removeAttribute('aria-valuemax');
        },
        set(done, total, label) {
            root.classList.remove('is-busy');
            const max = Math.max(0, total || 0);
            const value = Math.min(Math.max(0, done || 0), max);
            fill.style.width = `${max ? (value / max * 100).toFixed(1) : 0}%`;
            text.textContent = label ?? `${value}/${max}`;
            root.setAttribute('aria-valuemax', String(max));
            root.setAttribute('aria-valuenow', String(value));
        },
    };
}
