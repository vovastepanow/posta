// Pure indexes for a snapshot of the topic catalogue.
export const treeNorm = s => String(s ?? '').toLowerCase().replace(/ё/g, 'е').replace(/#/g, '').trim().replace(/[\s-]+/g, '_');
export const topicTags = t => [t.hashtags?.rubric, ...(t.hashtags?.entities || [])].filter(Boolean);

export function buildTopicTree(topics) {
    const ids = new Map(topics.map(t => [t.id, t]));
    const children = new Map(), rubrics = [], terms = new Map();
    for (const t of topics) {
        const parent = t.parent?.id;
        if (parent && ids.has(parent)) {
            if (!children.has(parent)) children.set(parent, []);
            children.get(parent).push(t);
        } else rubrics.push(t);
        terms.set(t.id, [t.title, ...(t.aliases || []), t.about, ...topicTags(t), ...(t.hashtags?.synonyms || [])].map(treeNorm));
    }
    const byTitle = (a, b) => a.title.localeCompare(b.title, 'ru');
    rubrics.sort((a, b) => (b.scope === 'broad') - (a.scope === 'broad') || byTitle(a, b));
    for (const list of children.values()) list.sort(byTitle);
    const roots = new Map(), branches = new Map();
    function rubricOf(t) {
        const seen = new Set();
        let root = t;
        while (root.parent?.id && ids.has(root.parent.id) && !seen.has(root.id)) {
            if (roots.has(root.id)) { root = roots.get(root.id); break; }
            seen.add(root.id);
            root = ids.get(root.parent.id);
        }
        // A malformed cycle must never lead to an infinite traversal.
        if (!seen.has(root.id)) for (const id of seen) roots.set(id, root);
        return root;
    }
    function descendants(t) {
        if (!branches.has(t.id)) {
            const result = [], seen = new Set(), stack = [t];
            while (stack.length) {
                const item = stack.pop();
                if (seen.has(item.id)) continue;
                seen.add(item.id);
                result.push(item);
                const kids = children.get(item.id) || [];
                for (let i = kids.length - 1; i >= 0; i--) stack.push(kids[i]);
            }
            branches.set(t.id, result);
        }
        return branches.get(t.id);
    }
    const matches = (t, q) => !q || (terms.get(t.id) || []).some(text => text.includes(q));
    return { ids, children, rubrics, rubricOf, descendants, matches };
}
