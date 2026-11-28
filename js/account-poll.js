// Each attempt owns its timer. Cancellation invalidates even an in-flight reply.
export function createPoller(delay = 2000, schedule = setTimeout, cancel = clearTimeout) {
    let generation = 0, timer;
    function stop() {
        ++generation;
        cancel(timer);
        timer = undefined;
    }
    function begin() {
        stop();
        const version = generation;
        const current = () => version === generation;
        function run(step) {
            if (!current()) return;
            const tick = async () => {
                if (!current()) return;
                await step(current);
                if (current()) timer = schedule(tick, delay);
            };
            timer = schedule(tick, delay);
        }
        return { current, run };
    }
    return { begin, stop };
}
