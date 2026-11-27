// Read a whole source section for both the explorer and audit export.
// A malformed cursor must never silently turn a partial result into a full list.
export async function collectSourcePages(fetchPage, { state, signal, maxPages = 100 } = {}) {
    const sources = new Map(), cursors = new Set();
    let after = '';
    for (let page = 0; page < maxPages; page++) {
        signal?.throwIfAborted();
        const result = await fetchPage(after);
        signal?.throwIfAborted();
        if (state === 'found' && result.snapshot_available === false) {
            return { sources: [], snapshotAvailable: false };
        }
        for (const item of result.sources || []) {
            if (item.source_id) sources.set(item.source_id, item);
        }
        const next = String(result.next_after || '');
        if (!next) return { sources: [...sources.values()], snapshotAvailable: true };
        if (next === after || cursors.has(next)) break;
        cursors.add(next);
        after = next;
    }
    throw new Error('Сервер вернул некорректную или слишком длинную пагинацию (' + state + ').');
}
