// Проверка адаптеров просмотрщика постов (js/post-adapters.js) без браузера. Запуск: node tools/test_post_view.mjs
// Вход — образцы из фикстур и формы данных из мест вызова; выход — нормализованный пост (см. комментарий в post-adapters.js).
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

// core.js читает location при загрузке; в Node достаточно адреса, без DOM
globalThis.location = new URL('http://127.0.0.1/index.html');
const { fromSourcePost, fromRecord, fromQueuePost, fromPublished, recordSlides, needsFetch } = await import('../js/post-adapters.js');

let failures = 0;
const check = (name, ok) => {
    console.log(`${ok ? 'OK ' : 'FAIL'} ${name}`);
    if (!ok) failures++;
};
const fixture = name => JSON.parse(readFileSync(fileURLToPath(new URL(`./fixtures/${name}`, import.meta.url)), 'utf8')).discovered;
const SHAPE = ['key', 'source', 'date', 'url', 'text', 'media', 'article', 'poll', 'stats', 'badge'];
const shapeOk = post => SHAPE.every(k => k in post) && Array.isArray(post.media) && typeof post.text === 'string';

console.log('A. Пост источника:');
const memes = fixture('source_preview_tg_memes.json');
const memesPosts = memes.posts.map(p => fromSourcePost(p, { name: memes.name, url: memes.stats?.url }));
check('все посты фикстуры мемов приводятся к формату', memesPosts.length === memes.posts.length && memesPosts.every(shapeOk));
const album = memesPosts.find(p => p.media.length > 1);
check('альбом: все файлы по порядку, картинки — image', album && album.media.every(m => m.kind === 'image' || m.kind === 'video'));
check('текст без медиа: пустая строка, не undefined', memesPosts.every(p => p.text === '' || typeof p.text === 'string'));
check('источник: имя переносится из ленты', memesPosts[0].source.name === memes.name);
check('ссылка поста — только https через safeSourcePostUrl', memesPosts.every(p => !p.url || p.url.startsWith('https://')));
const bad = fromSourcePost({ text: '  a\r\nb  ', url: 'http://t.me/x/1', media: [{ kind: 'image', src: 'https://cdn.example/a.jpg' }] });
check('небезопасный адрес поста отбрасывается, текст нормализуется переносами', bad.url === '' && bad.text === 'a\nb');
const video = fromSourcePost({ text: '', url: 'https://t.me/x/2', media: [{ kind: 'video', src: '', poster: 'https://cdn.example/p.jpg', page: 'https://t.me/x/2', pending: true }, { kind: 'video', unavailable: true }] });
check('видео: докачка и большое видео сохраняются флагами', video.media[0].pending === true && video.media[0].poster && video.media[1].unavailable === true);
const shorts = fromSourcePost({ text: '', url: 'https://www.youtube.com/shorts/tfbnG3nCUeo', media: [{ kind: 'video', src: '', poster: 'https://cdn.example/s.jpg', page: 'https://www.youtube.com/shorts/tfbnG3nCUeo', pending: false, unavailable: false }] });
check('шортс без файла и без докачки — скачивать, когда пост показан (не ссылка)', needsFetch(shorts.media[0], true) === true);
check('шортс в докачке — не скачивать повторно', needsFetch({ ...shorts.media[0], pending: true }, true) === false);
check('в месте вызова, которое следит само — не скачивать', needsFetch(shorts.media[0], false) === false);
check('ролик с файлом или чужая ссылка — не скачивать', needsFetch({ ...shorts.media[0], src: 'https://cdn.example/v.mp4' }, true) === false && needsFetch({ kind: 'video', page: 'https://example.com/v' }, true) === false);
const stats = fromSourcePost({ text: 'x', url: 'https://t.me/x/3', views: 77, reactions: 5, comments: null, media: [] });
check('статистика: просмотры, реакции, комментариев нет — null', stats.stats.views === 77 && stats.stats.reactions === 5 && stats.stats.comments === null);

console.log('Б. Запись подбора:');
const photo = { id: 'r1', url: 'https://vc.ru/x/1', text: 'Подпись', image: 'https://cdn.example/a.jpg', images: ['https://cdn.example/a.jpg', 'https://cdn.example/b.jpg'], posted_at: 1792740000 };
const rec = fromRecord(photo);
check('галерея: два файла по порядку, с запасным адресом', rec.media.length === 2 && rec.media[1].src === 'https://cdn.example/b.jpg' && rec.media[0].fallback === 'https://cdn.example/a.jpg');
check('дата записи — posted_at', rec.date === 1792740000 && rec.key === 'record:r1');
const textOnly = fromRecord({ id: 'r2', url: 'https://t.me/x/9', text: 'Просто текст', text_post: true });
check('текстовый пост без картинки: медиа нет, текст сохранён', textOnly.media.length === 0 && textOnly.text === 'Просто текст');
check('слайды текстового поста: один текстовый слайд', recordSlides({ text: 'a', text_post: true }).length === 1 && recordSlides({ text: 'a', text_post: true })[0].textPost === true);
const withVideo = fromRecord({ id: 'r3', url: 'https://t.me/x/5', text: 'v', video: 'https://cdn.example/v.mp4', image: 'https://cdn.example/p.jpg', video_unavailable: true, source_url: 'https://t.me/x/5' });
check('большое видео записи: unavailable у первого ролика', withVideo.media[0].kind === 'video' && withVideo.media[0].unavailable === true);
const engaged = fromRecord({ id: 'r4', url: 'https://t.me/x/6', text: 't', image: 'https://cdn.example/a.jpg', engagement: { source: 'telegram', views: 1200, reactions: { '🔥': 3, '👍': 9, '💩': 0 } } });
check('реакции записи: пары по убыванию, нулевые отброшены', engaged.stats.views === 1200 && JSON.stringify(engaged.stats.reactions) === JSON.stringify([['👍', 9], ['🔥', 3]]));
const article = fromRecord({ id: 'r5', url: 'https://vc.ru/x/7', source_url: 'https://vc.ru/x/7', source_key: 'rss:vc', text: 'Заголовок\nАнонс', article: { title: 'Заголовок', lead: 'Анонс', site: 'vc.ru', rubric: 'Транспорт', minutes: 4, cover: { src: 'https://cdn.example/c.jpg' } } });
check('статья: заголовок, сайт, обложка, адрес для article_read', article.article?.title === 'Заголовок' && article.article.cover === 'https://cdn.example/c.jpg' && article.article.readUrl === 'https://vc.ru/x/7' && article.article.minutes === 4);
check('без статьи — article null', rec.article === null);

console.log('В. Очередь календаря и журнал:');
const queued = fromQueuePost({ id: 'q1', text: 'В очереди пост', status: 'queued', at: 1792750000, thumb: 'https://app.example/outbox_media?id=q1', channel_title: 'Москва' }, { title: 'Москва' });
check('очередь: статус «в очереди», картинка — первая, канал по названию', queued.badge.text === 'в очереди' && queued.media.length === 1 && queued.source.name === 'Москва' && queued.date === 1792750000);
const failed = fromQueuePost({ id: 'q2', text: 't', status: 'failed', at: 1 });
check('очередь: «не вышел» — тон bad', failed.badge.text === 'не вышел' && failed.badge.tone === 'bad');
const poll = fromQueuePost({ id: 'q3', text: 'Опрос', status: 'queued', at: 2, poll: { question: 'Что выбрать?', options: ['А', 'Б'] } });
check('опрос из очереди: вопрос и варианты', poll.poll.question === 'Что выбрать?' && poll.poll.options.length === 2);
const published = fromPublished({ text: 'Вышло', media: 3, url: 'https://t.me/x/8', at: 1792700000 });
check('журнал: текст, ссылка, число файлов в значке', published.text === 'Вышло' && published.url === 'https://t.me/x/8' && published.badge.text.includes('3') && published.media.length === 0);

console.log(failures ? `\nПровалено: ${failures}` : '\nВсе проверки прошли');
process.exit(failures ? 1 : 0);
