// Имя, аватар и тариф владельца в боковом меню.
import { $, api } from './core.js';

/** Имя владельца: имя из Telegram, логин, название канала или «Владелец». */
function profileName(info) {
    const channel = info.channels?.[0];
    return info.auth?.tg_name || info.auth?.login || channel?.title || 'Владелец';
}

/** Заполняет имя и аватар (первая буква имени) по ответу account_info. */
export function showProfile(info) {
    const name = profileName(info);
    const nameEl = $('profile-name');
    const avatarEl = $('profile-avatar');
    if (nameEl) nameEl.textContent = name;
    if (avatarEl) avatarEl.textContent = (name[0] || 'P').toUpperCase();
}

/** Строка тарифа по ответу billing_info. */
export function showPlan(billing) {
    const planEl = $('profile-plan');
    if (planEl) planEl.textContent = `Тариф «${billing.plan?.title || '—'}»`;
}

// Coalesce concurrent header/home loads without caching session data across
// later requests. The server omits history, files and cookie diagnostics.
let profileRequest = null;
export function loadProfileSummary() {
    if (!profileRequest) profileRequest = api('account_info', { params: { summary: '1' } }).finally(() => { profileRequest = null; });
    return profileRequest;
}
