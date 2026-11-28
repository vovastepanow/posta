import assert from 'node:assert/strict';
import { addDays, calendarAgenda, dayAgenda, freeSlots, slotTimes, summary } from '../js/calendar-model.js';

const start = new Date(2026, 10, 23).getTime();
const end = addDays(start, 7);
const channel = { id: 'a', schedule: { slots: Object.fromEntries(Array.from({ length: 7 }, (_, i) => [i + 1, ['00:00', '10:00', '15:30']])) } };
const other = { ...channel, id: 'b' };
const channels = [channel, other];
const posts = [
    { id: 1, channel: 'a', at: (start - 60000) / 1000, status: 'sent' },
    { id: 2, channel: 'a', at: (start + 10 * 3600000 + 60000) / 1000, status: 'queued' },
    { id: 3, channel: 'b', at: (start + 10 * 3600000 + 61000) / 1000, status: 'telegram' },
    { id: 4, channel: 'a', at: (start + 15.5 * 3600000) / 1000, status: 'failed' },
    { id: 5, channel: 'c', at: (start + 15.5 * 3600000) / 1000, status: 'queued' },
    { id: 6, channel: 'b', at: (start + 15.5 * 3600000) / 1000, status: 'queued' },
];
const now = start - 120000;
function referenceSlots(c, from, to) {
    return slotTimes(c.schedule, from, to - 1)
        .filter(at => at > now && !posts.some(p => p.channel === c.id && Math.abs(p.at * 1000 - at) <= 60000))
        .map(at => ({ kind: 'slot', at, channel: c }));
}
function referenceDay(day) {
    const next = addDays(day, 1);
    return [
        ...posts.filter(p => channels.some(c => c.id === p.channel) && p.at * 1000 >= day && p.at * 1000 < next)
            .map(post => ({ kind: 'post', at: post.at * 1000, post })),
        ...channels.flatMap(c => referenceSlots(c, day, next)),
    ].sort((a, b) => a.at - b.at);
}
const agenda = calendarAgenda(posts, channels, now);
for (let d = start; d < end; d = addDays(d, 1)) {
    assert.deepEqual(agenda(d), referenceDay(d));
    assert.deepEqual(dayAgenda(posts, channels, d, now), referenceDay(d));
}
assert.deepEqual(agenda(start + 3600000), referenceDay(start + 3600000));
assert.deepEqual(freeSlots(channel, posts, start, end, now), referenceSlots(channel, start, end));
assert.deepEqual(summary(posts, channels, start, now), { queued: 3, freeThisWeek: channels.flatMap(c => referenceSlots(c, start, end)).length });
// Helsinki changes from UTC+2 to UTC+3 on March 29, 2026.
const spring = slotTimes({ tz: 'Europe/Helsinki', slots: { 6: ['10:00'], 7: ['10:00'] } }, Date.UTC(2026, 2, 28), Date.UTC(2026, 2, 30));
assert.deepEqual(spring, [Date.UTC(2026, 2, 28, 8), Date.UTC(2026, 2, 29, 7)]);
const autumn = slotTimes({ tz: 'Europe/Helsinki', slots: { 6: ['10:00'], 7: ['10:00'] } }, Date.UTC(2026, 9, 24), Date.UTC(2026, 9, 26));
assert.deepEqual(autumn, [Date.UTC(2026, 9, 24, 7), Date.UTC(2026, 9, 25, 8)]);
console.log('Calendar agenda, occupancy boundaries, order and DST checks passed.');
