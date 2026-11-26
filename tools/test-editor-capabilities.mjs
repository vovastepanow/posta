import assert from 'node:assert/strict';
import { commonCapabilities, commonPollLimits, validateTarget, mediaKind, attachmentError } from '../js/editor-capabilities.js';
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
