// Resource overrides are complete snapshots. Editing a resource does not
// mutate the shared post or another resource's media, poll or settings.
import { extraError, standaloneExtra, validateTarget, mediaKind, EXTRA_KEYS, extraActive,targetCapabilities } from './editor-capabilities.js';
import { plainStyledText } from './source-format.js';
export function cloneDraft(d={}) {
    return {text:String(d.text||''),media:(d.media||[]).map(m=>({...m,cover:m.cover?{...m.cover}:undefined})),tags:[...(d.tags||[])],
        poll:d.poll?{...d.poll,options:[...(d.poll.options||[])]}:null,extras:structuredClone(d.extras||{})};
}
export function draftFor(base,variants,target) {
    return Object.hasOwn(variants||{},target)?variants[target]:base;
}
export function draftPayload(d,target=null) {
    const poll=d.poll, separate=Boolean(poll&&!poll.with_content)||(!poll&&standaloneExtra(d.extras));
    const media=separate?[]:(d.media||[]).map(({kind,src,name,file,existing,cover})=>({kind,src,name,file,existing,cover}));
    let text=separate?'':String(d.text||'').trim();
    const extras=poll?null:structuredClone(d.extras||{});
    if(extras?.styled_text&&target&&!targetCapabilities(target)?.extras?.includes('styled_text')){text=plainStyledText(text);extras.styled_text=false;}
    const tags=poll||separate||d.extras?.ig_story||media.some(m=>['sticker','video_note'].includes(mediaKind(m)))?[]:[...(d.tags||[])];
    return {text,media,hashtags:tags,poll:poll?{...poll,question:poll.question.trim(),options:poll.options.map(v=>v.trim()).filter(Boolean)}:null,extras};
}
export function draftProblem(target,d) {
    const payload=draftPayload(d,target);
    if(!payload.poll&&standaloneExtra(payload.extras))return extraError([target],[], '',null,payload.extras);
    return validateTarget(target,payload.media,payload.text,payload.poll)||extraError([target],payload.media,payload.text,payload.poll,payload.extras);
}
export function draftNeedsQueue(d) {
    if(!d)return false;
    const p=draftPayload(d);
    return Boolean(p.poll)||p.media.some(m=>m.cover||['audio','document','voice','sticker','video_note'].includes(mediaKind(m)))||EXTRA_KEYS.some(k=>extraActive(p.extras,k));
}
export function draftHasContent(d) {
    return !!(d?.text?.trim()||d?.media?.length||d?.poll||standaloneExtra(d?.extras));
}

export function payloadAllowsSourceLink(p) {
    return !(p.poll&&!p.poll.with_content)&&!standaloneExtra(p.extras)&&!p.extras?.ig_story&&!(p.media||[]).some(m=>['sticker','video_note'].includes(mediaKind(m)));
}

/** Shared multipart contract for a new post and editing an existing queue item.
 * Only queue edits may refer to already stored attachments or covers. */
export function appendEditorMedia(form, media, { allowExisting = false } = {}) {
    const files = [];
    const manifest = media.map(({kind, name, file, existing, src, cover}) => {
        const item = { kind, name };
        if (file) item.upload = files.push(file) - 1;
        else if (allowExisting && existing !== undefined) item.existing = existing;
        else item.src = src;
        if (cover?.file) item.cover_upload = files.push(cover.file) - 1;
        else if (allowExisting && cover?.existing) item.cover_existing = true;
        return item;
    });
    form.append('media', JSON.stringify(manifest));
    for (const file of files) form.append('files[]', file);
    return files.length;
}
