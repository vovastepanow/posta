// Social connections: actual destinations, connection state and on-demand setup.
import { $, api, apiUrl, el, ask } from './core.js';

const ORDER = ['max', 'vk', 'ok', 'youtube', 'pinterest', 'tumblr', 'x'];
const LABEL = {max:'MAX',vk:'VK',ok:'OK',youtube:'YT',pinterest:'P',tumblr:'t',x:'X'};
const DESCRIPTIONS = {
    max:'Каналы и чаты · текст, фото, видео, файлы',
    vk:'Сообщества · текст, фото, видео, опросы',
    ok:'Группы · текст, фото, видео, опросы',
    youtube:'Видео и Shorts',
    pinterest:'Доски · фото и видео',
    tumblr:'Блоги · текст, фото, GIF и видео',
    x:'Профили · текст, фото, видео, опросы'
};
let networks = {}, channels = [], inactive = [];
let loadVersion = 0;
const status = text => { $('networks-status').textContent = text; };

export async function loadNetworks() {
    const version = ++loadVersion;
    try {
        const info = await api('networks_list');
        if (version !== loadVersion) return;
        networks = info.networks || {};
        channels = info.channels || [];
        inactive = info.inactive_channels || [];
        renderNetworks();
    } catch (error) {
        if (version === loadVersion) status('Не удалось загрузить соцсети: '+error.message);
    }
}
const logo = key => el('span', {className:'connection-icon connection-icon-'+key, ariaHidden:'true',textContent:LABEL[key] || key.slice(0,2).toUpperCase()});
const pill = (text, connected=false) => el('span',{className:'connection-state'+(connected?' is-connected':''),textContent:text});

function renderNetworks() {
    const connected = $('networks-connected');
    $('networks-count').textContent = String(channels.length);
    connected.replaceChildren(...(channels.length ? channels.map(c => el('div',{className:'connection-row'},
        logo(c.platform),el('div',{className:'connection-copy'},el('strong',{textContent:c.title || c.ref}),el('span',{textContent:networks[c.platform]?.title || c.platform})),
        pill('Подключено',true),el('button',{type:'button',className:'account-secondary',textContent:'Отключить',onclick:()=>removeChannel(c.id)})))
        : [el('p',{className:'connection-empty',textContent:'Пока нет подключённых ресурсов. Выберите площадку ниже.'})]));
    $('networks-list').replaceChildren(...ORDER.filter(key=>networks[key]?.publishing_in_scope).map(key=>{
        const n=networks[key], open=n.connect_available===true;
        const count=channels.filter(c=>c.platform===key).length;
        const state=open ? (n.status==='beta'?'Бета':'Доступно') : n.status==='needs_keys'?'Нужна настройка':'Бета · закрыто';
        const button=!open?null:n.oauth?el('a',{className:'secondary-button',href:apiUrl('network_oauth_start',{platform:key}),textContent:'Подключить'}):el('button',{type:'button',className:'secondary-button',textContent:'Подключить',onclick:()=>openForm(key)});
        return el('article',{className:'connection-tile'},el('div',{className:'connection-tile-head'},logo(key),
            el('div',{className:'connection-copy'},el('strong',{textContent:n.title}),el('span',{textContent:DESCRIPTIONS[key] || ''})),pill(state)),
            el('div',{className:'connection-tile-foot'},el('span',{className:'account-muted',textContent:count?count+' подключено':open?'':'Нет доступа к подключению'}),button));
    }));
    $('networks-inactive').hidden=!inactive.length;
    $('networks-inactive-list').replaceChildren(...inactive.map(c=>el('div',{className:'connection-row'},
        el('div',{className:'connection-copy'},el('strong',{textContent:c.title || c.ref}),el('span',{textContent:c.platform+' · больше не подключается'})),
        el('button',{type:'button',className:'account-secondary',textContent:'Отключить',onclick:()=>removeChannel(c.id)}))));
}

function openForm(key) {
    const n=networks[key];
    if (!n?.connect_available) return;
    const form=$('networks-form');
    const inputs=(n.fields || []).map(f=>el('label',{},el('span',{textContent:f.label}),el('input',{name:f.name,type:f.secret?'password':'text',autocomplete:'off',required:true})));
    const close=()=>{form.reset();form.hidden=true;form.replaceChildren();};
    form.replaceChildren(el('div',{className:'connection-form-head'},el('h4',{textContent:'Подключение · '+n.title}),el('button',{type:'button',className:'account-secondary',textContent:'Закрыть',onclick:close})),
        el('p',{className:'account-muted',textContent:n.note}),...inputs,el('button',{type:'submit',className:'primary-button',textContent:'Проверить и подключить'}));
    form.hidden=false;
    form.scrollIntoView({block:'nearest'});
    form.querySelector('input')?.focus({preventScroll:true});
    form.onsubmit=async event=>{
        event.preventDefault();
        const button=event.submitter; if(button?.disabled)return;
        if(button)button.disabled=true;
        const fields=Object.fromEntries([...form.querySelectorAll('input')].map(input=>[input.name,input.value]));
        status('Проверяю доступ…');
        try {
            const info=await api('network_connect',{body:{platform:key,fields}});
            channels=info.channels || []; inactive=info.inactive_channels || [];
            window.dispatchEvent(new Event('posta:resources-changed'));
            close();status(n.title+' подключён.');renderNetworks();
        } catch(error){status(error.message);}
        finally{form.querySelectorAll('input[type=password]').forEach(i=>i.value='');if(button)button.disabled=false;}
    };
}

async function removeChannel(id) {
    if (!await ask('Отключить ресурс? История публикаций сохранится.')) return;
    try {
        const info=await api('network_remove',{body:{id}});
        channels=info.channels || [];inactive=info.inactive_channels || [];
        window.dispatchEvent(new Event('posta:resources-changed'));
        status('Ресурс отключён.');renderNetworks();
    } catch(error){status(error.message);}
}

export function initializeNetworks() {
    $('networks-form').hidden=true;
    const url=new URL(location.href);
    if(url.searchParams.has('network') || url.searchParams.has('network_error')){
        status(url.searchParams.get('network_error') || 'Соцсеть подключена.');
        for(const key of ['network','network_error','platform'])url.searchParams.delete(key);
        history.replaceState(null,'',url);
    }
}
