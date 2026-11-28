import assert from 'node:assert/strict';
import { networkCapabilities, commonCapabilities, commonPollLimits, targetTextLimit, validateTarget, mediaKind, attachmentError, extraError } from '../js/editor-capabilities.js';
const tg={value:'tg:main'}, yt={value:'youtube:1',capabilities:{platform:'youtube',kinds:['video'],mediaMax:1,textLimit:[5000,5000],poll:false}};
const x={value:'x:1',capabilities:{platform:'x',kinds:['photo','video','animation'],mediaMax:4,textLimit:[280,280],poll:false}};
assert.deepEqual(commonCapabilities([tg,yt]),{kinds:['video'],mediaMax:1,poll:false});
assert.equal(commonCapabilities([tg]).poll,true);
assert.equal(commonCapabilities([{value:'tg:main',unconnected:true}]).poll,false);
assert.equal(validateTarget(tg,[],'',{question:'?',options:['a','b']}),'');
assert.notEqual(validateTarget(x,[],'',{question:'?',options:['a','b']}),'');
assert.notEqual(validateTarget(yt,[],'text',null),'');
assert.equal(validateTarget(yt,[{kind:'video'}],'title',null),'');
assert.notEqual(validateTarget(x,[{kind:'image'},{kind:'video'}],'text',null),'');
assert.equal(mediaKind({kind:'image',name:'abc.GIF'}),'animation');
assert.equal(validateTarget(x,[],'😀'.repeat(280),null),'');
assert.notEqual(validateTarget(x,[],'😀'.repeat(281),null),'');
assert.notEqual(validateTarget({value:'unknown:1'},[],'text',null),'');
console.log('editor capability tests passed');

assert.equal(validateTarget(tg,[{kind:'audio'}],'caption',null),'');
assert.notEqual(validateTarget(tg,[{kind:'document'},{kind:'image'}],'caption',null),'');
assert.equal(validateTarget(tg,[{kind:'document'},{kind:'document'}],'caption',null),'');

const xp={...x,capabilities:{...x.capabilities,poll:true,pollLimits:{question_max:280,options_max:4,option_max:25,duration:true}}};
assert.equal(commonPollLimits([tg,xp]).options_max,4);
assert.equal(commonPollLimits([tg,xp]).multiple,false);
assert.equal(validateTarget(xp,[],'',{question:'Vote?',options:['Yes','No'],duration_minutes:60}),'');
assert.notEqual(validateTarget(xp,[],'',{question:'Vote?',options:['Yes','No'],multiple:true}),'');
assert.notEqual(validateTarget(xp,[],'',{question:'Vote?',options:['Yes','Yes']}),'');
assert.notEqual(validateTarget(xp,[],'',{question:'Vote?',options:['Yes','x'.repeat(26)]}),'');

assert.equal(attachmentError([x],[{kind:'photo'}],{kind:'photo'}),'');
assert.notEqual(attachmentError([x],[{kind:'photo'}],{kind:'video'}),'');
assert.notEqual(attachmentError([tg,yt],[],{kind:'audio'}),'');
assert.equal(attachmentError([tg],[{kind:'audio'}],{kind:'audio'}),'');
assert.notEqual(attachmentError([tg],[{kind:'audio'}],{kind:'document'}),'');
assert.notEqual(attachmentError([yt],[{kind:'video'}],{kind:'video'}),'');

const mx={value:'max:1',capabilities:{platform:'max',kinds:['photo','video','animation','audio','document'],mediaMax:12,textLimit:[4000,4000]}};
assert.equal(attachmentError([mx],[],{kind:'audio'}),'');
assert.notEqual(attachmentError([mx],[{kind:'audio'}],{kind:'audio'}),'');
assert.equal(attachmentError([tg],[],{kind:'voice'}),'');
assert.notEqual(attachmentError([tg],[{kind:'voice'}],{kind:'photo'}),'');
assert.equal(attachmentError([tg,mx],[],{kind:'document'}),'');

assert.equal(validateTarget(tg,[],'',{question:'Q',options:['A','B'],quiz:true,correct_answer:'A'}),'');
assert.notEqual(validateTarget(tg,[],'',{question:'Q',options:['A','B'],quiz:true,correct_answer:'C'}),'');
assert.notEqual(validateTarget(xp,[],'',{question:'Q',options:['A','B'],quiz:true,correct_answer:'A'}),'');

assert.equal(extraError([tg],[],'',null,{location:{latitude:'0',longitude:'0'}}),'');
assert.notEqual(extraError([tg],[],'',null,{location:{latitude:'',longitude:'0'}}),'');
assert.notEqual(extraError([tg],[],'',null,{contact:{first_name:'A',phone:'123'}}),'');

assert.equal(extraError([tg],[],'',null,{contact:{first_name:'A',phone:'+3581234567'}}),'');

assert.equal(targetTextLimit(tg, 1), 1024);
assert.equal(targetTextLimit(tg, 0), 4096);
assert.equal(targetTextLimit(yt, 1), 5000);
assert.equal(targetTextLimit(x, 0), 280);

// A resource may disable platform features; false and an empty array are intentional overrides.
const network = { kinds: ['photo'], media_max: 4, text_limit: [100, 200], max_bytes: { photo: 1000 }, poll: true, poll_limits: { options_max: 4 }, extras: ['first_comment'] };
assert.deepEqual(networkCapabilities('vk', network), { platform: 'vk', kinds: ['photo'], mediaMax: 4, textLimit: [100, 200], maxBytes: { photo: 1000 }, poll: true, pollLimits: { options_max: 4 }, extras: ['first_comment'] });
const restricted = networkCapabilities('vk', network, { poll_available: false, editor_extras: [] });
assert.equal(restricted.poll, false);
assert.deepEqual(restricted.extras, []);
assert.equal(networkCapabilities('vk', null), null);
assert.equal(network.poll, true);
