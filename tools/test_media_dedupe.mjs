// Проверка JS-версии дедупликации (js/media-dedupe.js) теми же векторами, что tools/test_media_dedupe.php. Запуск: node tools/test_media_dedupe.mjs
import { mediaDedupeUrls, mediaFileKey } from '../js/media-dedupe.js';

let failures = 0;
const check = (name, ok) => { console.log(`${ok ? 'OK   ' : 'FAIL '}${name}`); if (!ok) failures++; };
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);

const three = ['https://cdn4.telesco.pe/file/AbCdEfGhIjKlMnOpQrSt_s.jpg', 'https://cdn4.telesco.pe/file/AbCdEfGhIjKlMnOpQrSt_x.jpg', 'https://cdn5.telesco.pe/file/AbCdEfGhIjKlMnOpQrSt.jpg'];
check('дедупликация: три варианта одного файла — оригинал без суффикса', same(mediaDedupeUrls(three), ['https://cdn5.telesco.pe/file/AbCdEfGhIjKlMnOpQrSt.jpg']));
check('дедупликация: два суффикса — побеждает _x над _s', same(mediaDedupeUrls(three.slice(0, 2)), ['https://cdn4.telesco.pe/file/AbCdEfGhIjKlMnOpQrSt_x.jpg']));
check('дедупликация: параметр width побеждает меньший', same(mediaDedupeUrls(['https://a.test/p/pic.jpg?width=320', 'https://a.test/p/pic.jpg?width=1280']), ['https://a.test/p/pic.jpg?width=1280']));
check('дедупликация: разные файлы не склеиваются', mediaDedupeUrls(['https://a.test/x/one.jpg', 'https://a.test/x/two.jpg']).length === 2);
check('дедупликация: миниатюра того же файла не побеждает оригинал', same(mediaDedupeUrls(['https://a.test/p/photo_small.jpg', 'https://a.test/p/photo.jpg']), ['https://a.test/p/photo.jpg']));
check('дедупликация: разные файлы с похожими именами не склеиваются', mediaDedupeUrls(['https://a.test/p/thumb_small.jpg', 'https://a.test/p/photo.jpg']).length === 2);
check('дедупликация: порядок первого вхождения сохраняется', same(mediaDedupeUrls(['https://a.test/b.jpg', 'https://a.test/a.jpg', 'https://a.test/b_s.jpg']), ['https://a.test/b.jpg', 'https://a.test/a.jpg']));
check('дедупликация: пустые и не-строки отбрасываются', same(mediaDedupeUrls(['', null, 'https://a.test/c.jpg', undefined]), ['https://a.test/c.jpg']));
check('ключ: длинный id CDN совпадает между доменами', mediaFileKey('https://cdn4.x.org/file/AbCdEfGhIjKlMnOpQrSt.jpg') === mediaFileKey('https://cdn5.x.org/file/AbCdEfGhIjKlMnOpQrSt.jpg'));

console.log(failures ? `\nПровалено: ${failures}` : '\nВсе проверки прошли');
process.exit(failures ? 1 : 0);
