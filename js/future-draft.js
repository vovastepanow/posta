// Один собираемый пост хранится в браузере, включая добавленные пользователем файлы.
const DB = 'posta-future-draft';
let queue = Promise.resolve();
function database() {
    return new Promise((resolve, reject) => {
        const request = indexedDB.open(DB, 1);
        request.onupgradeneeded = () => request.result.createObjectStore('drafts');
        request.onsuccess = () => resolve(request.result);
        request.onerror = () => reject(request.error);
    });
}
async function operation(mode, action) {
    const db = await database();
    try {
        return await new Promise((resolve, reject) => {
            const tx = db.transaction('drafts', mode);
            const req = action(tx.objectStore('drafts'));
            tx.oncomplete = () => resolve(req.result);
            tx.onerror = () => reject(tx.error);
            tx.onabort = () => reject(tx.error || new Error('Не удалось сохранить черновик'));
        });
    } finally { db.close(); }
}
export function readFutureDraft(key) {
    return queue.then(() => operation('readonly', store => store.get(key)));
}
export function saveFutureDraft(key, draft) {
    const snapshot = structuredClone(draft);
    const task = queue.catch(() => {}).then(() => operation('readwrite', store => store.put(snapshot, key)));
    queue = task;
    return task;
}
export function clearFutureDraft(key) {
    const task = queue.catch(() => {}).then(() => operation('readwrite', store => store.delete(key)));
    queue = task;
    return task;
}
