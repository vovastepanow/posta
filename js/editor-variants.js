// Resource overrides are complete snapshots. Editing a resource does not
// mutate the shared post or another resource's media, poll or settings.
import { extraError, standaloneExtra, validateTarget, mediaKind, EXTRA_KEYS, extraActive } from './editor-capabilities.js';
export function cloneDraft(d={}) {
    return {text:String(d.text||''),media:(d.media||[]).map(m=>({...m,cover:m.cover?{...m.cover}:undefined})),tags:[...(d.tags||[])],
        poll:d.poll?{...d.poll,options:[...(d.poll.options||[])]}:null,extras:structuredClone(d.extras||{})};
}
export function draftFor(base,variants,target) {
    return Object.hasOwn(variants||{},target)?variants[target]:base;
}
export function draftPayload(d) {
    const poll=d.poll, separate=Boolean(poll&&!poll.with_content)||(!poll&&standaloneExtra(d.extras));
    const media=separate?[]:(d.media||[]).map(({kind,src,name,file,existing,cover})=>({kind,src,name,file,existing,cover}));
    const text=separate?'':String(d.text||'').trim();
    const tags=poll||separate||d.extras?.ig_story||media.some(m=>['sticker','video_note'].includes(mediaKind(m)))?[]:[...(d.tags||[])];
    return {text,media,hashtags:tags,poll:poll?{...poll,question:poll.question.trim(),options:poll.options.map(v=>v.trim()).filter(Boolean)}:null,extras:poll?null:structuredClone(d.extras||{})};
}
export function draftProblem(target,d) {
    const payload=draftPayload(d);
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
