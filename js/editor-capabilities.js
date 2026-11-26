// UI preflight mirrors implemented delivery capabilities; the server is authoritative.
const TG = {platform:'telegram', kinds:['photo','video','animation'], mediaMax:10, textLimit:[1024,4096], poll:true};
const REQUIRED = new Set(['youtube','pinterest','instagram']);
export const mediaKind = m => m.kind === 'image' ? (/^image\/gif$/i.test(m.file?.type || '') || /\.gif(?:[?#]|$)/i.test(m.src || '') || /\.gif$/i.test(m.name || '') ? 'animation' : 'photo') : m.kind;
export function targetCapabilities(target) {
    if (!target || target.unconnected) return null;
    if (target.capabilities) return target.capabilities;
    return target.value?.startsWith('tg:') ? TG : null;
}
export function commonCapabilities(targets) {
    const caps = targets.map(targetCapabilities);
    const known = caps.length > 0 && caps.every(Boolean);
    return { kinds: known ? ['photo','video','animation','audio','document'].filter(k => caps.every(c => c.kinds?.includes(k))) : [],
        mediaMax: known ? Math.min(10, ...caps.map(c => c.mediaMax || 0)) : 0,
        poll: known && caps.every(c => c.poll === true) };
}
export function validateTarget(target, media, text, poll) {
    const c = targetCapabilities(target);
    if (!c) return 'Возможности ресурса недоступны. Проверьте подключение.';
    if (poll) return c.poll ? '' : 'Опросы не поддерживаются.';
    const kinds = media.map(mediaKind);
    // Telegram converts GIFs in albums to video; other adapters validate the original format.
    const effective = c.platform === 'telegram' && kinds.length > 1 ? kinds.map(k => k === 'animation' ? 'video' : k) : kinds;
    const limit = c.textLimit?.[media.length ? 0 : 1] || 4096;
    if ([...text].length > limit) return 'Текст превышает лимит ' + limit + ' символов.';
    if (media.length > (c.mediaMax || 0)) return 'Допустимо файлов: ' + (c.mediaMax || 0) + '.';
    if (effective.some(k => !c.kinds?.includes(k))) return 'Не поддерживается формат одного или нескольких вложений.';
    if (REQUIRED.has(c.platform) && !media.length) return 'Необходимо медиа.';
    if (['facebook','x'].includes(c.platform) && media.length > 1 && effective.some(k => k !== 'photo')) return 'Допустимы фотоальбом или один видеофайл/GIF (если поддерживается).';
    if (c.platform === 'tumblr' && effective.filter(k => k === 'video').length > 1) return 'Допустимо только одно видео.';
    if (['youtube','pinterest'].includes(c.platform) && [...text.trim().split(/\r?\n/)[0]].length > 100) return 'Первая строка (заголовок) длиннее 100 символов.';
    if (media.some((m,i) => m.file && c.maxBytes?.[effective[i]] > 0 && m.file.size > c.maxBytes[effective[i]])) return 'Размер файла превышает лимит ресурса.';
    if (!media.length && !text.trim()) return 'Добавьте текст или медиа.';
    return '';
}
