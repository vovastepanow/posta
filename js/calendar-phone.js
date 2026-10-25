// calendar-phone.js — телефонный экран календаря (< 700 px): полоса недели, список дня, шторка «Предложка».
// Данные и действия приходят из calendar.js через ctx, чтобы не тянуть его состояние сюда.
import { el, PHOTO_ICON, postTypeLabel, VIDEO_ICON } from './core.js';
import { addDays, dayAgenda, summary, weekStart } from './calendar-model.js';
import { shortText } from './card.js';

const WEEK = ['пн', 'вт', 'ср', 'чт', 'пт', 'сб', 'вс'];
const hhmm = ms => { const d = new Date(ms); return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`; };
const sameDay = (a, b) => new Date(a).toDateString() === new Date(b).toDateString();

/** Экран телефона: ctx = { day, posts, channels, best, now, pool, select(day), openPost(post, el), openSlot(row, el), openFill(), openPool(), laneOf(id), statusOf(post) }. */
export function phoneView(ctx) {
    const { day, posts, channels, now } = ctx;
    const view = el('div', { className: 'cal-phone' });
    const sum = summary(posts, channels, weekStart(day), now);
    view.append(header(ctx, sum), weekStrip(ctx), dayHeading(ctx), dayList(ctx), sheet(ctx));
    return view;
}

function header(ctx, sum) {
    const month = new Date(ctx.day).toLocaleString('ru-RU', { month: 'long' });
    return el('header', { className: 'cal-phone-head' },
        el('div', { className: 'cal-phone-top' },
            el('h1', { textContent: month.charAt(0).toUpperCase() + month.slice(1) }),
            el('span', { textContent: `${sum.queued} в очереди · ${sum.freeThisWeek} дыр` })));
}

/** Семь дней недели выбранного дня: сегодня — белая кнопка, выбранный — тёмный; до трёх точек цветов каналов. */
function weekStrip(ctx) {
    const start = weekStart(ctx.day);
    const days = Array.from({ length: 7 }, (_, i) => addDays(start, i));
    return el('div', { className: 'cal-phone-week', role: 'group', 'aria-label': 'Неделя' }, ...days.map(d => {
        const dots = [...new Set(ctx.posts.filter(p => sameDay(p.at * 1000, d)).map(p => p.channel))].slice(0, 3);
        const classes = ['cal-phone-day', sameDay(d, ctx.day) ? 'is-selected' : '', sameDay(d, ctx.now) ? 'is-today' : ''].filter(Boolean).join(' ');
        const button = el('button', { type: 'button', className: classes, 'aria-pressed': String(sameDay(d, ctx.day)), onclick: () => ctx.select(d) },
            el('span', { className: 'cal-phone-wd', textContent: WEEK[(new Date(d).getDay() + 6) % 7] }),
            el('b', { textContent: String(new Date(d).getDate()) }),
            el('span', { className: 'cal-phone-dots' }, ...dots.map(id => el('i', { 'aria-hidden': 'true' }))));
        button.querySelectorAll('.cal-phone-dots i').forEach((dot, i) => { dot.style.background = ctx.laneOf(dots[i]).color; });
        return button;
    }));
}

function dayHeading(ctx) {
    const d = new Date(ctx.day);
    const name = d.toLocaleString('ru-RU', { weekday: 'long' });
    return el('div', { className: 'cal-phone-title' },
        el('h2', { textContent: `${name.charAt(0).toUpperCase() + name.slice(1)}, ${d.getDate()}` }),
        el('span', { textContent: 'свайп — соседний день' }));
}

/** Список дня: время слева, карточки 64 px и слоты 48 px; слот в лучший час — с пометкой. Свайп по списку — соседний день. */
function dayList(ctx) {
    const rows = dayAgenda(ctx.posts, ctx.channels, ctx.day, ctx.now);
    const list = el('div', { className: 'cal-phone-list' }, ...(rows.length ? rows.map(row => row.kind === 'slot' ? slotRow(ctx, row) : postRow(ctx, row.post)) : [el('p', { className: 'cal-muted', textContent: 'Постов и свободных слотов в этот день нет.' })]));
    let startX = null;
    list.addEventListener('touchstart', e => { startX = e.touches[0].clientX; }, { passive: true });
    list.addEventListener('touchend', e => {
        if (startX === null) return;
        const dx = e.changedTouches[0].clientX - startX;
        startX = null;
        if (Math.abs(dx) > 60) ctx.select(addDays(ctx.day, dx < 0 ? 1 : -1));
    });
    return list;
}

function postRow(ctx, post) {
    const lane = ctx.laneOf(post.channel);
    const past = ctx.past(post);
    // миниатюра во всю высоту: картинка, камера или кавычка; плашка статуса внизу, как в сетке
    const media = el('span', { className: 'cal-phone-media' });
    if (post.thumb_url) media.append(el('img', { className: 'cal-thumb', src: ctx.thumbUrl(post), alt: '', loading: 'lazy' }));
    else if (post.media > 0) media.append(el('span', { className: 'cal-ph', title: `Медиа: ${post.media}`, 'aria-hidden': 'true', innerHTML: post.kind === 'video' ? VIDEO_ICON : PHOTO_ICON }));
    else media.append(el('span', { className: 'cal-quote', textContent: '“', 'aria-hidden': 'true' }));
    const plate = ctx.plate(post);
    if (plate) media.append(el('span', { className: `cal-plate ${plate[1]}`, textContent: plate[0] }));
    const card = el('button', { type: 'button', className: `cal-phone-card${post.article ? ' is-article' : ''}${past ? ' is-past' : ''}`, onclick: event => ctx.openPost(post, event.currentTarget) },
        media,
        el('span', { className: 'cal-phone-main' },
            post.article   // статья: как будущий пост в Telegram — жирный заголовок и анонс
                ? el('span', { className: 'cal-phone-text is-article' }, el('b', { textContent: post.article.title }), post.article.lead ? el('span', { className: 'cal-phone-lead', textContent: shortText(post.article.lead, 140) }) : null)
                : el('span', { className: 'cal-phone-text', textContent: post.error && post.status === 'failed' ? post.error : (post.text || postTypeLabel(post)) }),
            el('span', { className: 'cal-phone-meta' }, ctx.avatar(post.channel, 'sm', false), el('span', { textContent: `${lane.title} · ${ctx.statusOf(post)}` }))));
    card.style.setProperty('--c', lane.color);
    return el('div', { className: 'cal-phone-row' }, el('span', { className: 'cal-phone-time', textContent: hhmm(post.at * 1000) }), card);
}

function slotRow(ctx, row) {
    const best = ctx.best.includes(new Date(row.at).getHours());
    const note = best ? 'свободно · лучшее время канала' : 'свободно';
    return el('div', { className: 'cal-phone-row' },
        el('span', { className: 'cal-phone-time', textContent: hhmm(row.at) }),
        el('button', { type: 'button', className: `cal-phone-slot${best ? ' is-best' : ''}`, onclick: event => ctx.openSlot(row, event.currentTarget) },
            el('span', { textContent: note }),
            el('span', { className: 'cal-phone-plus', textContent: '+', 'aria-hidden': 'true' })));
}

/** Шторка внизу: «Предложка · N» (открывает список Предложки) и «Заполнить дыры». */
function sheet(ctx) {
    return el('footer', { className: 'cal-phone-sheet' },
        el('button', { type: 'button', className: 'cal-phone-pool', onclick: () => ctx.openPool(), textContent: `Предложка · ${ctx.pool}` }),
        el('button', { type: 'button', className: 'cal-phone-fill', onclick: () => ctx.openFill(), textContent: 'Заполнить дыры' }));
}

export const isPhone = () => window.matchMedia('(max-width: 700px)').matches;
