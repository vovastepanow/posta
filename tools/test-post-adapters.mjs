import assert from 'node:assert/strict';

globalThis.location = new URL('https://posta.example/posta/');
globalThis.document = { addEventListener() {} };
const { fromRecord, recordSlides, hasRecordContent, analyzePost, recordMediaIncomplete } = await import('../js/post-adapters.js');

const video = { video: 'https://example.org/v.mp4', images: [''], text_post: true };
assert.equal(hasRecordContent(video), true);
assert.equal(recordSlides(video)[0].video, video.video);
assert.equal(fromRecord(video).media[0].kind, 'video');

const pending = { video_via: 'ytdlp', video_page: 'https://www.youtube.com/shorts/abcdefghijk', images: [''] };
assert.equal(hasRecordContent(pending), true);
assert.equal(fromRecord(pending).media[0].kind, 'video');
assert.equal(fromRecord(pending).media[0].pending, true);
assert.equal(fromRecord({ ...pending, video_unavailable: true }).media[0].unavailable, true);

assert.equal(hasRecordContent({ text_post: true, text: 'A text post' }), true);
assert.equal(fromRecord({ text_post: true, text: 'A text post' }).media.length, 0);
assert.equal(hasRecordContent({ images: [''] }), false);
assert.equal(recordSlides({ images: ['', 'https://example.org/photo.jpg'] }).length, 1);
console.log('Coverless video and text record checks passed.');

const mixed = fromRecord({source_url: 'https://t.me/publicchannel/1', text: 'Полный текст', media_incomplete: true, text_incomplete: true, gallery: [{image: 'https://example.org/photo.jpg'}, {video: 'https://example.org/2.mp4'}, {video: 'https://example.org/3.mp4'}]});
assert.equal(mixed.media.length, 3);
assert.equal(mixed.media[1].src, 'https://example.org/2.mp4');
assert.equal(mixed.media[2].src, 'https://example.org/3.mp4');
assert.ok(analyzePost(mixed).warnings.some(x => x.includes('вложения')));
assert.ok(analyzePost(mixed).warnings.some(x => x.includes('часть текста')));

const posterVideo = fromRecord({...pending, images: [], gallery: [{image: 'https://example.org/poster.jpg'}]});
assert.equal(posterVideo.media[0].kind, 'video');
assert.equal(posterVideo.media[0].poster, 'https://example.org/poster.jpg');
assert.equal(posterVideo.media[0].page, pending.video_page);

assert.equal(recordMediaIncomplete({image:'https://example.org/cover.jpg', expected_video_count:1}), true);
assert.equal(recordMediaIncomplete({video_unavailable:true, image:'https://example.org/cover.jpg'}), true);
assert.equal(recordMediaIncomplete({gallery:[{image:'https://example.org/photo.jpg'},{video:'https://example.org/a.mp4'},{video:'https://example.org/b.mp4'}], expected_video_count:2}), false);

const selected = fromRecord({source:'Telegram | Example channel', text:'Complete text', why_relevant:'Matches the topic', posted_at:123, gallery:[{image:'https://example.org/a.jpg'},{video:'https://example.org/a.mp4'}]});
assert.equal(selected.source.name, 'Example channel');
assert.equal(selected.selectionReason, 'Matches the topic');
assert.equal(selected.media.length, 2);
assert.equal(selected.media[1].kind, 'video');
assert.equal(fromRecord({text:'No review'}).selectionReason, '');
