// theme.js — оформление «День» и «Эфир»: выбор пользователя в localStorage, без выбора — по часам (Эфир с 19:00 до 07:00).

const KEY = 'postaTheme';
const THEME_COLORS = { day: '#FFF4D6', night: '#14151C' };
const RECHECK_MS = 10 * 60 * 1000;   // в авто-режиме время проверяем раз в 10 минут

/** Сохранённый выбор или null (авто). Приватное окно может бросить исключение — тогда выбор не запоминается. */
function savedTheme() {
    try {
        const theme = localStorage.getItem(KEY);
        return theme === 'day' || theme === 'night' ? theme : null;
    } catch { return null; }
}

function saveTheme(theme) {
    try { localStorage.setItem(KEY, theme); } catch { /* без хранилища выбор действует до перезагрузки */ }
}

function clockTheme(now = new Date()) {
    const hour = now.getHours();
    return hour >= 19 || hour < 7 ? 'night' : 'day';
}

function applyTheme(theme) {
    document.documentElement.dataset.theme = theme;
    document.querySelector('meta[name="theme-color"]')?.setAttribute('content', THEME_COLORS[theme]);
    document.querySelectorAll('[data-theme-set]').forEach(button => button.setAttribute('aria-pressed', String(button.dataset.themeSet === theme)));
}

export function initializeTheme() {
    applyTheme(savedTheme() ?? clockTheme());
    document.querySelectorAll('[data-theme-set]').forEach(button => button.addEventListener('click', () => {
        saveTheme(button.dataset.themeSet);   // повторное нажатие той же темы ничего не меняет
        applyTheme(button.dataset.themeSet);
    }));
    setInterval(() => { if (savedTheme() === null) applyTheme(clockTheme()); }, RECHECK_MS);
}
