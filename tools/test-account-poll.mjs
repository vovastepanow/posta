import assert from 'node:assert/strict';
import { createPoller } from '../js/account-poll.js';
let sequence = 0;
const scheduled = new Map();
const poller = createPoller(2000, (fn, delay) => {
    assert.equal(delay, 2000);
    scheduled.set(++sequence, fn);
    return sequence;
}, id => scheduled.delete(id));
const tick = () => {
    const [id, fn] = scheduled.entries().next().value;
    scheduled.delete(id);
    return fn();
};
const first = poller.begin();
let resolve, count = 0;
first.run(async current => {
    await new Promise(r => { resolve = r; });
    if (current()) count++;
});
const pending = tick();
assert.equal(scheduled.size, 0); // no overlapping status requests
poller.stop();
assert.equal(first.current(), false);
const second = poller.begin();
second.run(async current => { if (current()) count++; });
resolve();
await pending;
assert.equal(count, 0);
assert.equal(scheduled.size, 1); // stale completion must not schedule another request
await tick();
assert.equal(count, 1);
assert.equal(scheduled.size, 1);
poller.stop();
assert.equal(scheduled.size, 0);
assert.equal(second.current(), false);
console.log('account polling cancellation tests passed');
