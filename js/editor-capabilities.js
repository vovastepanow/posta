// UI preflight mirrors implemented delivery capabilities; the server is authoritative.
const TG = {platform:'telegram', kinds:['photo','video','animation','audio','document'], maxBytes:{audio:50_000_000,document:50_000_000}, mediaMax:10, textLimit:[1024,4096], poll:true,pollLimits:{question_max:300,options_max:10,option_max:100,multiple:true}};
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
export function commonPollLimits(targets) {
    const caps = targets.map(targetCapabilities);
    if (!caps.length || !caps.every(c => c?.poll && c?.pollLimits)) return null;
    const limits = caps.map(c => c.pollLimits);
    return {question_max:Math.min(...limits.map(l=>l.question_max)),options_max:Math.min(...limits.map(l=>l.options_max)),
        option_max:Math.min(...limits.map(l=>l.option_max)),multiple:limits.every(l=>l.multiple), duration:limits.some(l=>l.duration)};
}
export function validateTarget(target, media, text, poll) {
    const c = targetCapabilities(target);
    if (!c) return 'Возможности ресурса недоступны. Проверьте подключение.';
    if (poll) {
        if (!c.poll || !c.pollLimits) return 'Опросы не поддерживаются.';
        const l = c.pollLimits, options = (poll.options || []).map(s=>s.trim()).filter(Boolean);
        if (![...poll.question.trim()].length || [...poll.question.trim()].length > l.question_max) return 'Вопрос: от 1 до ' + l.question_max + ' символов.';
        if (options.length < 2 || options.length > l.options_max) return 'Варианты: от 2 до ' + l.options_max + '.';
        if (options.some(o=>[...o].length > l.option_max)) return 'Вариант ответа: не более ' + l.option_max + ' символов.';
        if (new Set(options.map(o=>o.toLowerCase())).size !== options.length) return 'Варианты ответа должны различаться.';
        if (poll.multiple && !l.multiple) return 'Множественный выбор не поддерживается.';
        const duration = Number(poll.duration_minutes || 1440);
        if (l.duration && (!Number.isInteger(duration) || duration < 5 || duration > 10080)) return 'Длительность: от 5 минут до 7 дней.';
        return '';
    }
    const kinds = media.map(mediaKind);
    // Telegram converts GIFs in albums to video; other adapters validate the original format.
    const effective = c.platform === 'telegram' && kinds.length > 1 ? kinds.map(k => k === 'animation' ? 'video' : k) : kinds;
    const limit = c.textLimit?.[media.length ? 0 : 1] || 4096;
    if ([...text].length > limit) return 'Текст превышает лимит ' + limit + ' символов.';
    if (media.length > (c.mediaMax || 0)) return 'Допустимо файлов: ' + (c.mediaMax || 0) + '.';
    if (effective.some(k => !c.kinds?.includes(k))) return 'Не поддерживается формат одного или нескольких вложений.';
    if (REQUIRED.has(c.platform) && !media.length) return 'Необходимо медиа.';
    if (['facebook','x'].includes(c.platform) && media.length > 1 && effective.some(k => k !== 'photo')) return 'Допустимы фотоальбом или один видеофайл/GIF (если поддерживается).';
    if (c.platform === 'telegram' && effective.some(k => ['audio','document'].includes(k)) && new Set(effective).size > 1) return 'Аудио и PDF нельзя смешивать с другими типами вложений.';
    if (c.platform === 'tumblr' && effective.filter(k => k === 'video').length > 1) return 'Допустимо только одно видео.';
    if (['youtube','pinterest'].includes(c.platform) && [...text.trim().split(/\r?\n/)[0]].length > 100) return 'Первая строка (заголовок) длиннее 100 символов.';
    if (media.some((m,i) => m.file && c.maxBytes?.[effective[i]] > 0 && m.file.size > c.maxBytes[effective[i]])) return 'Размер файла превышает лимит ресурса.';
    if (!media.length && !text.trim()) return 'Добавьте текст или медиа.';
    return '';
}

// Use the same validation for attachment controls as for final submission.
export function attachmentError(targets, media, item) {
    if (!targets.length) return 'Выберите ресурс.';
    for (const target of targets) {
        const error = validateTarget(target, [...media, item], '', null);
        if (error) return `${target?.label || target?.value || 'Ресурс'}: ${error}`;
    }
    return '';
}
