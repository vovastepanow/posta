// networks.js — другие соцсети в «Личном кабинете»: список, вход (OAuth) или поля подключения из реестра, отключение.
// Статусы, поля и признак входа приходят с сервера (networks_public).
import { $, api, apiUrl, el } from './core.js';

const STATUS = { ready: 'можно подключить', beta: 'бета: проверяется, подключает владелец', planned: 'скоро',
    needs_keys: 'не настроено на сервере (нужны ключи приложения)', unavailable: 'недоступно', manual: 'через Telegram-канал' };
let networks = {};
let channels = [];

/** Список: подключённые ресурсы и соцсети из реестра с кнопкой подключения. */
export async function loadNetworks() {
    try {
        const info = await api('networks_list');
        networks = info.networks;
        channels = info.channels;
        renderNetworks();
    } catch (error) {
        $('networks-status').textContent = `Не удалось загрузить соцсети: ${error.message}`;
    }
}

function renderNetworks() {
    const items = channels.map(c => el('div', { className: 'channel-row' },
        el('b', { textContent: `${networks[c.platform]?.title ?? c.platform} «${c.title || c.ref}»` }),
        el('button', { type: 'button', className: 'secondary-button', textContent: 'Отключить', onclick: () => removeChannel(c.id) })));
    for (const [key, network] of Object.entries(networks)) {
        if (key === 'telegram') continue;
        const open = ['ready', 'beta'].includes(network.status);
        const button = !open ? null : network.oauth
            ? el('a', { className: 'secondary-button', href: apiUrl('network_oauth_start', { platform: key }), textContent: `Войти в ${network.title}` })
            : el('button', { type: 'button', className: 'secondary-button', textContent: 'Подключить', onclick: () => openForm(key) });
        items.push(el('div', { className: 'social-item' }, el('b', { textContent: network.title }),
            el('p', { className: 'account-muted', textContent: `${STATUS[network.status] ?? network.status}. ${network.note}` }), button));
    }
    $('networks-list').replaceChildren(...items);
}

/** Форма подключения без входа: поля из реестра; секреты — полем пароля, после отправки очищаются. */
function openForm(key) {
    const network = networks[key];
    const form = $('networks-form');
    const inputs = (network.fields ?? []).map(f => el('label', {}, el('span', { textContent: f.label }),
        el('input', { name: f.name, type: f.secret ? 'password' : 'text', autocomplete: 'off', required: true })));
    form.replaceChildren(el('p', { className: 'account-muted', textContent: network.note }), ...inputs,
        el('button', { type: 'submit', className: 'primary-button', textContent: `Подключить ${network.title}` }));
    form.hidden = false;
    form.onsubmit = async event => {
        event.preventDefault();
        const fields = Object.fromEntries([...form.querySelectorAll('input')].map(input => [input.name, input.value]));
        const status = $('networks-status');
        status.textContent = 'Проверяю доступ…';
        try {
            const info = await api('network_connect', { body: { platform: key, fields } });
            channels = info.channels;
            form.hidden = true;
            status.textContent = `${network.title} подключён.`;
            renderNetworks();
        } catch (error) {
            status.textContent = error.message;
        } finally {
            form.querySelectorAll('input').forEach(input => { input.value = ''; });
        }
    };
}

async function removeChannel(id) {
    try {
        const info = await api('network_remove', { body: { id } });
        channels = info.channels;
        $('networks-status').textContent = 'Ресурс отключён.';
        renderNetworks();
    } catch (error) {
        $('networks-status').textContent = error.message;
    }
}

/** После входа провайдер возвращает сюда: показываем итог и убираем параметры из адреса. */
export function initializeNetworks() {
    $('networks-form').hidden = true;
    const params = new URLSearchParams(location.search);
    if (params.has('network') || params.has('network_error')) {
        $('networks-status').textContent = params.has('network_error') ? params.get('network_error') : 'Соцсеть подключена.';
        history.replaceState(null, '', location.pathname);
    }
}
