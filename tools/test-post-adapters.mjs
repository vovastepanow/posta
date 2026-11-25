import assert from 'node:assert/strict';

globalThis.location = new URL('https://posta.example/posta/');
globalThis.document = { addEventListener() {} };
const { fromRecord, recordSlides, hasRecordContent } = await import('../js/post-adapters.js');

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
