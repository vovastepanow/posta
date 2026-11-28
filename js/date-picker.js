// Выбор времени публикации, общий для календаря, редактора и кабинета.
import { el } from './core.js';

// ---- Дата и время ----

const MONTHS = ['Январь', 'Февраль', 'Март', 'Апрель', 'Май', 'Июнь', 'Июль', 'Август', 'Сентябрь', 'Октябрь', 'Ноябрь', 'Декабрь'];
const MIN_LEAD_MS = 60000;   // как на сервере: минимум минута вперёд
const pad = n => String(n).padStart(2, '0');
const sameDay = (a, b) => a.toDateString() === b.toDateString();
const button = (className, textContent, onclick, extra = {}) => el('button', { type: 'button', className, textContent, onclick, ...extra });

/** «Сегодня, 20:30», «Завтра, 10:00», «25 октября, 10:00». */
export function formatWhen(date) {
    const time = `${pad(date.getHours())}:${pad(date.getMinutes())}`;
    const today = new Date();
    if (sameDay(date, today)) return `Сегодня, ${time}`;
    if (sameDay(date, new Date(today.getFullYear(), today.getMonth(), today.getDate() + 1))) return `Завтра, ${time}`;
    return `${date.getDate()} ${MONTHS[date.getMonth()].toLowerCase().replace(/[ьй]$/, 'я').replace(/т$/, 'та')}, ${time}`;
}

/** → Promise: Date — выбрано время, null — «Сразу», undefined — закрыто без выбора. */
export function pickDateTime({ value = null } = {}) {
    return new Promise(resolve => {
        const now = new Date();
        const roundHour = () => new Date(Math.ceil((now.getTime() + 3600000) / 300000) * 300000);
        const selected = new Date(value && value > now ? value : roundHour());
        const view = new Date(selected.getFullYear(), selected.getMonth(), 1);
        const focus = document.activeElement;
        const [title, grid, summary] = [el('span', { className: 'dtp-title' }), el('div', { className: 'dtp-grid' }), el('div', { className: 'dtp-summary' })];
        const select = (label, n) => el('select', { className: 'dtp-select', ariaLabel: label }, ...Array.from({ length: n }, (_, i) => el('option', { value: i, textContent: pad(i) })));
        const [hours, minutes] = [select('Часы', 24), select('Минуты', 60)];
        const ok = () => selected.getTime() >= Date.now() + MIN_LEAD_MS;
        const close = result => { document.removeEventListener('keydown', onKey, true); root.remove(); focus?.focus?.(); resolve(result); };
        const onKey = e => { if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); close(undefined); } };
        const done = button('dtp-done', 'Готово', () => ok() && close(new Date(selected)));
        const refresh = () => {
            title.textContent = `${MONTHS[view.getMonth()]} ${view.getFullYear()}`;
            const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
            const days = new Date(view.getFullYear(), view.getMonth() + 1, 0).getDate();
            grid.replaceChildren(...['Пн', 'Вт', 'Ср', 'Чт', 'Пт', 'Сб', 'Вс'].map(d => el('span', { className: 'dtp-weekday', textContent: d })),
                ...Array.from({ length: (view.getDay() + 6) % 7 }, () => el('span')),
                ...Array.from({ length: days }, (_, i) => {
                    const date = new Date(view.getFullYear(), view.getMonth(), i + 1);
                    return button(`dtp-day${sameDay(date, today) ? ' is-today' : ''}${sameDay(date, selected) ? ' is-selected' : ''}`, String(i + 1),
                        () => { selected.setFullYear(date.getFullYear(), date.getMonth(), date.getDate()); refresh(); }, { disabled: date < today });
                }));
            hours.value = String(selected.getHours());
            minutes.value = String(selected.getMinutes());
            summary.textContent = ok() ? formatWhen(selected) : 'Выберите время хотя бы на минуту вперёд';
            summary.classList.toggle('is-error', !ok());
            done.disabled = !ok();
        };
        const month = delta => { view.setMonth(view.getMonth() + delta); refresh(); };
        const at = (days, hour) => new Date(now.getFullYear(), now.getMonth(), now.getDate() + days, hour, 0);
        const presets = [['Через час', roundHour()], ['Сегодня 20:00', at(0, 20)], ['Завтра 10:00', at(1, 10)], ['Завтра 20:00', at(1, 20)]].filter(([, d]) => d.getTime() >= now.getTime() + MIN_LEAD_MS);
        hours.onchange = () => { selected.setHours(Number(hours.value)); refresh(); };
        minutes.onchange = () => { selected.setMinutes(Number(minutes.value)); refresh(); };
        const root = el('div', { className: 'dtp-overlay', role: 'dialog', ariaModal: 'true', ariaLabel: 'Выбор даты и времени' }, el('div', { className: 'dtp-window' },
            el('div', { className: 'dtp-head' }, button('dtp-nav', '‹', () => month(-1), { ariaLabel: 'Предыдущий месяц' }), title, button('dtp-nav', '›', () => month(1), { ariaLabel: 'Следующий месяц' })),
            grid,
            presets.length ? el('div', { className: 'dtp-presets' }, ...presets.map(([label, d]) => button('dtp-chip', label, () => { selected.setTime(d.getTime()); view.setFullYear(d.getFullYear(), d.getMonth(), 1); refresh(); }))) : null,
            el('div', { className: 'dtp-time' }, el('span', { textContent: 'Время' }), hours, el('b', { textContent: ':' }), minutes),
            summary,
            el('div', { className: 'dtp-actions' }, button('dtp-now', 'Сразу', () => close(null)), button('dtp-cancel', 'Отмена', () => close(undefined)), done)));
        root.addEventListener('click', e => { e.stopPropagation(); if (e.target === root) close(undefined); });   // окно внутри панели публикации: клик её не закрывает
        document.addEventListener('keydown', onKey, true);
        document.body.append(root);
        refresh();
        done.focus();
    });
}
