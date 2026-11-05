// profile.js — имя, аватар и тариф владельца в нижней части бокового меню (студия и приложение).
// Без зависимостей: страница студии не грузит core.js.

const $ = id => document.getElementById(id);

/** Имя владельца: имя из Telegram, логин, название канала или «Владелец». */
export function profileName(info) {
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
