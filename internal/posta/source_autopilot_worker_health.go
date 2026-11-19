package posta

import (
    "context"
    "log"
    "time"
)

// This is process-local liveness, not an assertion that a source was
// approved. It lets the authenticated status API distinguish a dormant
// worker from a valid but idle PostgreSQL queue, without a schema change.
type autopilotWorkerHealth struct {
    LastTick int64
    LastPanic int64
    Panics int64
    ReconcileError bool
}

func (a *App) recordAutopilotWorkerTick(mode string, err error) {
    a.autopilotWorkerMu.Lock()
    defer a.autopilotWorkerMu.Unlock()
    if a.autopilotWorkerStates == nil {
        a.autopilotWorkerStates = make(map[string]autopilotWorkerHealth)
    }
    state := a.autopilotWorkerStates[mode]
    state.LastTick = time.Now().Unix()
    state.ReconcileError = err != nil
    a.autopilotWorkerStates[mode] = state
}

func (a *App) recordAutopilotWorkerPanic(mode string) {
    a.autopilotWorkerMu.Lock()
    defer a.autopilotWorkerMu.Unlock()
    if a.autopilotWorkerStates == nil {
        a.autopilotWorkerStates = make(map[string]autopilotWorkerHealth)
    }
    state := a.autopilotWorkerStates[mode]
    state.Panics++
    state.LastPanic = time.Now().Unix()
    a.autopilotWorkerStates[mode] = state
}

func (a *App) autopilotWorkerHealth(mode string) autopilotWorkerHealth {
    a.autopilotWorkerMu.Lock()
    defer a.autopilotWorkerMu.Unlock()
    return a.autopilotWorkerStates[mode]
}

func autopilotWorkerAlive(health autopilotWorkerHealth, now int64) bool {
    return health.LastTick > 0 && now >= health.LastTick && now-health.LastTick <= 180
}

// App.Go normally logs a panic and then permanently ends that goroutine.
// Source-autopilot schedulers must instead restart after a transient panic;
// a panic must never silently kill all future discovery on this server.
func (a *App) superviseAutopilotWorker(ctx context.Context, mode string, worker func(context.Context)) {
    for ctx.Err() == nil {
        func() {
            defer func() {
                if recovered := recover(); recovered != nil {
                    a.recordAutopilotWorkerPanic(mode)
                    log.Printf("автопилот %s: авария фонового планировщика (перезапуск): %v", mode, recovered)
                } else if ctx.Err() == nil {
                    log.Printf("автопилот %s: фоновый планировщик неожиданно завершился; перезапуск", mode)
                }
            }()
            worker(ctx)
        }()
        if ctx.Err() != nil {
            return
        }
        select {
        case <-ctx.Done():
            return
        case <-time.After(time.Minute):
        }
    }
}
