// Profile suggestions are literal requests, not a hierarchy.
export function filterSearchProfiles(profiles, query, current) {
    const key = String(query || '').trim().toLocaleLowerCase('ru-RU');
    return profiles.filter(p => String(p.title || '').toLocaleLowerCase('ru-RU').includes(key))
        .slice().sort((a,b) => Number(b.id === current) - Number(a.id === current) || a.title.localeCompare(b.title, 'ru'));
}
