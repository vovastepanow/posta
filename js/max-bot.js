// The shared service bot is separate from the MAX publishing connection.
import { $, api, el, ask } from './core.js';

let version=0;
const status=text=>{$('max-bot-status').textContent=text;};
const btn=(text,action,primary=false)=>el('button',{type:'button',className:primary?'primary-button':'account-secondary',textContent:text,onclick:action});
const link=(text,url)=>el('a',{className:'secondary-button',href:url,target:'_blank',rel:'noopener noreferrer',textContent:text});
async function busy(button,fn) {
    if(button.disabled)return;
    button.disabled=true;
    try{await fn();}catch(e){status(e.message);}finally{button.disabled=false;}
}

export async function loadMaxBot(){
    const seq=++version;
    try{
        const data=await api('max_bot_status');
        if(seq!==version)return;
        render(data);
    }catch(error){if(seq===version)status('Не удалось проверить бота MAX: '+error.message);}
}
function render(data){
    const body=$('max-bot-body');
    const pill=el('span',{className:'connection-state'+(data.linked?' is-connected':''),textContent:data.linked?'Привязан':data.configured?'Доступен':'Не настроен'});
    const actions=el('div',{className:'max-bot-actions'},pill);
    if(data.configured){
        actions.append(link('Открыть бота',data.url));
        if(data.linked){
            const unlink=btn('Отключить',async()=>{if(!await ask('Отключить аккаунт MAX от профиля Posta?'))return;await busy(unlink,async()=>{await api('max_bot_unlink',{body:null});status('Привязка удалена.');await loadMaxBot();});});
            actions.append(unlink);
        }else{
            const start=btn('Привязать аккаунт',()=>busy(start,async()=>{
                const result=await api('max_bot_link_start',{body:null});
                showLink(result);
            }),true);
            actions.append(start);
        }
    }
    body.replaceChildren(actions,el('p',{className:'account-muted',textContent:data.linked?'В боте доступны команды /start, /status и /help.':data.configured?'Откройте бота по одноразовой ссылке и подтвердите код в этом кабинете.':'Владелец сервиса должен подключить общего бота. Для обычных пользователей токен не нужен.'}));
    if(data.owner){
        const token=el('input',{type:'password',name:'token',autocomplete:'off',placeholder:data.username?'Новый токен или пусто для повтора':'Вставьте токен созданного бота',required:!data.username});
        // Use the real external browser URL rather than proxy headers or an
        // internal localhost address from the installation configuration.
        const current=new URL('./',location.href);
        const external=current.protocol==='https:' && current.hostname.includes('.') && !current.hostname.endsWith('.local') && !current.hostname.endsWith('.localhost') && !/^\d+\.\d+\.\d+\.\d+$/.test(current.hostname) && !current.port;
        const site=el('input',{type:'url',name:'site_url',autocomplete:'url',value:data.site_url || (external?current.href:'https://posta.rocks/'),placeholder:'https://posta.rocks/',required:true,readOnly:data.configured===true});
        const submit=el('button',{type:'submit',className:'primary-button',textContent:data.configured?'Проверить и обновить подключение':'Подключить общего бота'});
        const form=el('form',{className:'telegram-form'},el('label',{},el('span',{textContent:'Публичный HTTPS-адрес Posta'}),site),
            el('p',{className:'account-muted',textContent:'Укажите адрес, по которому Posta открывается через интернет. Вебхук будет создан автоматически. Адрес localhost не подходит.'}),
            el('label',{},el('span',{textContent:'Токен MAX для бизнеса'}),token),submit);
        form.onsubmit=e=>{e.preventDefault();busy(submit,async()=>{
            status('Проверяю бота и HTTPS-вебхук…');
            try{await api('max_bot_configure',{body:{token:token.value,site_url:site.value.trim()}});status('Бот подключён.');await loadMaxBot();}
            catch(error){
                // If identity verification succeeded, the encrypted token was
                // already saved even if registration failed. Permit a retry.
                const saved=await api('max_bot_status').catch(()=>null);
                if(saved?.username){token.required=false;token.placeholder='Токен сохранён — можно повторить без ввода';}
                throw error;
            }
            finally{token.value='';}
        });};
        body.append(el('details',{className:'max-bot-owner',open:!data.configured},el('summary',{textContent:'Настройка владельца'+(data.username?' · @'+data.username:'')}),el('p',{className:'account-muted',textContent:'Токен хранится зашифрованно. Posta проверит его и зарегистрирует защищённый вебхук автоматически.'}),form));
    }
}
function showLink(result){
    const code=el('input',{type:'text',name:'code',autocomplete:'one-time-code',maxLength:8,pattern:'[A-Fa-f0-9]{8}',placeholder:'8 символов из сообщения бота',required:true});
    const submit=el('button',{type:'submit',className:'primary-button',textContent:'Подтвердить привязку'});
    const form=el('form',{className:'telegram-form max-bot-link-steps'},
        el('p',{textContent:'1. Откройте бота и нажмите «Старт». Ссылка действует 10 минут.'}),
        link('Перейти в MAX',result.url),el('label',{},el('span',{textContent:'2. Код из сообщения бота'}),code),
        el('div',{className:'max-bot-actions'},submit,btn('Отмена',()=>{status('');loadMaxBot();})));
    form.onsubmit=e=>{e.preventDefault();busy(submit,async()=>{
        await api('max_bot_link_confirm',{body:{token:result.token,code:code.value.trim()}});
        status('Аккаунт MAX привязан.');await loadMaxBot();
    });};
    $('max-bot-body').replaceChildren(form);
    status('Ссылка готова. Не пересылайте её другим людям.');
}
