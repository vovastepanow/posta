import assert from 'node:assert/strict';
import {cloneDraft,draftFor,draftPayload,draftProblem,draftNeedsQueue,draftHasContent,payloadAllowsSourceLink} from '../js/editor-variants.js';
const base={text:'A',media:[{kind:'photo',src:'https://example.com/a.jpg'}],tags:['#a'],poll:null,extras:{}};
const tg={value:'tg:main'};
const x={value:'x:1',capabilities:{platform:'x',kinds:['photo'],mediaMax:4,textLimit:[280,280],poll:false,extras:['first_comment']}};
const variants={'tg:main':cloneDraft(base)};
variants['tg:main'].media.push({kind:'photo',src:'https://example.com/b.jpg'});
variants['tg:main'].text='B';
variants['tg:main'].extras.styled_text=true;
assert.equal(base.media.length,1);
assert.equal(draftFor(base,variants,x.value).text,'A');
assert.equal(draftPayload(draftFor(base,variants,tg.value)).media.length,2);
assert.equal(draftProblem(tg,variants['tg:main']),'');
assert.equal(draftProblem(x,base),'');
assert.equal(draftProblem(x,variants['tg:main']),'');
assert.equal(draftPayload({...base,text:'**Bold** [Read](https://example.com/a)',extras:{styled_text:true}},x).text,'Bold Read (https://example.com/a)');
assert.equal(draftNeedsQueue(variants['tg:main']),true);
assert.equal(draftNeedsQueue(base),false);
const card={...cloneDraft(base),extras:{location:{latitude:'0',longitude:'0'}}};
assert.equal(draftPayload(card).media.length,0);
assert.equal(draftPayload(card).text,'');
assert.equal(draftProblem(tg,card),'');
assert.equal(draftHasContent({extras:{max_sticker:'a'}}),true);
assert.equal(draftPayload({...cloneDraft(base),poll:{question:'Q',options:['A','B']}}).media.length,0);
console.log('editor variant isolation tests passed');

assert.equal(payloadAllowsSourceLink(draftPayload(base)),true);
assert.equal(payloadAllowsSourceLink(draftPayload({...base,extras:{ig_story:true}})),false);
assert.equal(payloadAllowsSourceLink(draftPayload(card)),false);
assert.equal(payloadAllowsSourceLink(draftPayload({...base,media:[{kind:'video_note'}]})),false);
assert.equal(payloadAllowsSourceLink(draftPayload({...base,poll:{question:'Q',options:['A','B']}})),false);
assert.equal(payloadAllowsSourceLink(draftPayload({...base,poll:{question:'Q',options:['A','B'],with_content:true}})),true);

import {editorRecordKey} from '../js/source-format.js';
assert.notEqual(editorRecordKey({text:'First text'}),editorRecordKey({text:'Second text'}));
assert.notEqual(editorRecordKey({video:'a',image:'same'}),editorRecordKey({video:'b',image:'same'}));
assert.equal(editorRecordKey({source_key:'telegram:abcde',post_id:'12',image:'old'}),editorRecordKey({source_key:'telegram:abcde',post_id:'12',image:'new'}));
assert.notEqual(editorRecordKey({source_key:'telegram:abcde',post_id:'12'}),editorRecordKey({source_key:'telegram:other',post_id:'12'}));

import {editorArticleURL,editorDestinationSelection} from '../js/source-format.js';
assert.equal(editorArticleURL({url:'https://vc.ru/123456',source_url:'https://example.com/feed'}),'https://vc.ru/123456');
assert.equal(editorArticleURL({url:'javascript:alert(1)',source_url:'https://example.com/a'}),'https://example.com/a');
const destinations=[{value:'tg:main'},{value:'vk:group'},{value:'tg:second'}];
assert.deepEqual(editorDestinationSelection(destinations,['primary','group','second'],'primary'),['tg:main','vk:group','tg:second']);
assert.throws(()=>editorDestinationSelection(destinations,['missing'],'primary'));

assert.notEqual(editorRecordKey({url:'https://example.org/1',source_url:'https://example.org/feed'}),editorRecordKey({url:'https://example.org/2',source_url:'https://example.org/feed'}));
