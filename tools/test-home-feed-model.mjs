import assert from 'node:assert/strict';
import { buildHomeFeed, reuseFeedItems } from '../js/home-feed-model.js';
const items = [
    {item_key:'text',text:'Текст',text_post:true,timestamp:3,source:'Telegram | Example',relevance:20},
    {item_key:'photo',text:'Фото',display_image:'/fixture.png',timestamp:2,relevance:90,hashtags:['кот']},
    {item_key:'video',text:'Видео',video_via:'ytdlp',video_pending:true,timestamp:1,relevance:30},
    {item_key:'gallery',text:'Альбом',gallery:[{image:'a'},{video:'b',image:'poster'}],timestamp:4,relevance:40}
];
const index = buildHomeFeed(items);
const keys = rows => rows.map(r => r.item.item_key);
assert.deepEqual(index.filters.map(r=>[r.id,r.count]),[['all',4],['images',1],['videos',2],['text',1],['telegram',1],['boards',0]]);
assert.deepEqual(keys(index.visible('all','new','')),['gallery','text','photo','video']);
assert.deepEqual(keys(index.visible('all','rel','')),['photo','gallery','video','text']);
assert.deepEqual(keys(index.visible('all','new','КОТ')),['photo']);
assert.deepEqual(keys(index.visible('videos','new','')),['gallery','video']);
const copy = items.map(i => structuredClone(i));
copy[1].text = 'Changed';
const reused = reuseFeedItems(items, copy);
assert.equal(reused[0],items[0]);
assert.equal(reused[1],copy[1]);
assert.equal(reused[2],items[2]);
assert.deepEqual(reuseFeedItems([items[0],copy[0]],[copy[0],copy[0]]),[items[0],copy[0]]);
console.log('Home feed classification, ordering, filtering and identity passed.');
