package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"flame/tools/sandbox"
	"flame/tools/xlog"
)

var (
	errPoolClosed = errors.New("script pool closed")
	errPoolBusy   = errors.New("sandbox busy")
)

// slotPool is the per-language worker slot pool plus the concurrency gate.
//
// Design points:
//   - sem is the concurrency gate (capacity = max_concurrent). Past it, acquire waits for the timeout and then
//     reports "busy"; it never blocks indefinitely — binding transforms and the like are nested calls, where an
//     unbounded wait would deadlock against itself.
//   - The pooled object is the resident codehost host (running inside an isolation unit): the unit is reused and
//     codehost forks a brand-new interpreter per execute — process-level cleanliness with naturally zero
//     residue.
//   - The return policy has a single tier: a clean worker ("one execute completed") goes back for reuse, a
//     half-finished one (interrupted / timed out / protocol error) is destroyed. The interpreter process is new
//     every time anyway, so nothing leaks across requests.
//   - The refiller keeps idle.initial idle objects: without it the pool drains and the warm-up was pointless. It
//     idles when idle.initial is 0.
type slotPool struct {
	lang           string
	cfg            LangConfig
	isolate        sandbox.Sandbox
	codehostBinary string         // binary entry of the resident codehost host
	tmpRoot        string         // host root of the writable directories: each worker gets an exclusive subdirectory
	limits         sandbox.Limits // resource caps used when starting a worker (from script.limits, clamped)

	idle      chan *Worker
	sem       chan struct{}
	kick      chan struct{} // immediate refill signal for the refiller (capacity 1, extra signals merge)
	closed    chan struct{}
	closeOnce sync.Once
}

// refillInterval is the refiller's backstop sweep interval: immediate refills go through kick, this only heals
// (a worker dying on its own, or a retry after a failed refill).
const refillInterval = time.Second

// newSlotPool creates a per-language slot pool.
//
// The pool exists as long as the language entry does (code.pools.<lang> present = this pool present); "no warm-up
// / not resident" is expressed by the idle numbers (both 0 keeps the pool idling by itself).
func newSlotPool(lang string, cfg LangConfig, iso sandbox.Sandbox, codehostBinary, tmpRoot string, limits sandbox.Limits) *slotPool {
	p := &slotPool{
		lang:           lang,
		cfg:            cfg,
		isolate:        iso,
		codehostBinary: codehostBinary,
		tmpRoot:        tmpRoot,
		limits:         limits,
		sem:            make(chan struct{}, cfg.MaxConcurrent),
		kick:           make(chan struct{}, 1),
		closed:         make(chan struct{}),
		idle:           make(chan *Worker, cfg.Idle.Max),
	}

	// The pool exists as long as the language entry does: refiller / janitor start unconditionally.
	// With idle.initial = 0, refill returns at the top (idling) and the janitor only faces an empty pool.
	p.startRefiller()
	p.startJanitor()
	return p
}

// startRefiller keeps idle.initial idle objects: warm-up at startup plus refills during the run.
//
// Every return of a "half-finished" worker destroys it, and without refilling the pool would drain, after which
// every request degrades to "spawn on the spot" (which is exactly what the warm-up was for).
func (p *slotPool) startRefiller() {
	go func() {
		p.refill()
		xlog.Info("sandboxd: %s pool ready (%s=%d/%d)",
			p.lang, p.idleWhat(), p.idleCount(), p.cfg.Idle.Initial)
		t := time.NewTicker(refillInterval)
		defer t.Stop()
		for {
			select {
			case <-p.closed:
				return
			case <-p.kick:
			case <-t.C:
			}
			p.refill()
		}
	}()
}

// idleWhat is the name of the idleness metric (log only).
func (p *slotPool) idleWhat() string { return "idle" }

// idleCount is the number of idle objects.
func (p *slotPool) idleCount() int { return len(p.idle) }

// refill tops the idle objects up to idle.initial (created synchronously: this function only runs in the refiller
// goroutine, so the cold-start cost never lands on a request path).
func (p *slotPool) refill() {
	target := p.cfg.Idle.Initial
	if target <= 0 {
		return
	}

	if p.idle == nil {
		return
	}
	for len(p.idle) < target {
		select {
		case <-p.closed:
			return
		default:
		}
		w, err := spawnCodehost(p.isolate, p.codehostBinary, p.tmpRoot, p.limits)
		if err != nil {
			xlog.Error("sandboxd: refill %s codehost failed: %v", p.lang, err)
			return // do not spin on a failure, wait for the next kick / sweep
		}
		select {
		case p.idle <- w:
		case <-p.closed:
			w.Destroy()
			return
		default:
			w.Destroy() // the pool is full (concurrent returns), discard
			return
		}
	}
}

// kickRefill asks the refiller to top up as soon as possible (non-blocking: a meaningful signal is enough).
func (p *slotPool) kickRefill() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// startJanitor periodically clears dead idle workers.
//
// It only "clears"; refilling belongs to the refiller (both converge on the same idle pool).
func (p *slotPool) startJanitor() {
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				p.dropDeadIdle()
			case <-p.closed:
				return
			}
		}
	}()
}

// dropDeadIdle probes the idle workers one by one, discards the dead and puts the live ones back.
//
// This is the only place that still pings actively (once every 30s, off the request path): what it has to find is
// a "process alive but no longer answering" fake death that watch cannot see (codehost wedged, for instance).
func (p *slotPool) dropDeadIdle() {
	var keep []*Worker
loop:
	for {
		select {
		case w := <-p.idle:
			if w.ping() == nil {
				keep = append(keep, w)
			} else {
				w.Destroy()
			}
		default:
			break loop
		}
	}
	for _, w := range keep {
		select {
		case p.idle <- w:
		default:
			w.Destroy()
		}
	}
	p.kickRefill() // refill as many as were dropped (with restart, idle pool members may die on their own too)
}

// Acquire lends out one worker; reused reports whether an instance from the idle pool was reused.
func (p *slotPool) Acquire(ctx context.Context) (*Worker, bool, error) {
	// 1) The concurrency gate (with a timeout, never an unbounded block)
	wait := time.Duration(p.cfg.AcquireTimeoutSec) * time.Second
	if wait <= 0 {
		// Fallback: the config layer (applyDefaults) always fills in the default (15s); this only covers the
		// impossible-in-production path of "the default was deleted" — the value matches config.go so there is no
		// second copy of the number.
		wait = 15 * time.Second
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case <-p.closed:
		return nil, false, errPoolClosed
	case <-timer.C:
		return nil, false, fmt.Errorf("%w: %s concurrency already at the limit of %d", errPoolBusy, p.lang, p.cfg.MaxConcurrent)
	}

	// 2) Prefer reusing an idle worker.
	//    No ping here: one liveness probe is a synchronous "write a frame + wait for pong" round trip, while lua is
	//    a µs-level high-frequency call (loop conditions / branch conditions / binding transforms), and that fixed
	//    cost would exceed the script itself. Liveness is maintained by Worker.watch (alive goes false as soon as
	//    the process exits), with the janitor pinging every 30s as a backstop.
	if p.idle != nil {
	loop:
		for {
			select {
			case cand := <-p.idle:
				if cand.Alive() {
					return cand, true, nil
				}
				cand.Destroy()
				p.kickRefill()
			default:
				break loop
			}
		}
	}

	// 3) Spawn on the spot
	w, err := spawnCodehost(p.isolate, p.codehostBinary, p.tmpRoot, p.limits)
	if err != nil {
		<-p.sem
		return nil, false, err
	}
	return w, false, nil
}

// Release returns a worker.
//
// A single tier: reusable == false (an interrupted, timed-out, protocol-error or abnormal-connection execution)
// destroys it — a worker in an unknown state is never reused; only a clean one whose process is still alive (alive
// as marked by watch) goes back to the idle pool.
// The return does not ping either: if the process had exited, watch would already have set alive to false and it is
// destroyed right here.
// The interpreter process is new every time anyway, so nothing leaks across requests and the lifecycle tiering
// (reuse / restart) is gone.
func (p *slotPool) Release(w *Worker, reusable bool) {
	<-p.sem // release the concurrency quota first

	if w == nil {
		return
	}

	select {
	case <-p.closed:
		p.destroyWorker(w)
		return
	default:
	}

	if !reusable || p.idle == nil || !w.Alive() {
		w.Destroy()
		p.kickRefill()
		return
	}

	select {
	case p.idle <- w:
	case <-p.closed:
		w.Destroy()
	default:
		w.Destroy() // the pool is full
	}
}

// destroyWorker destroys a worker for good.
func (p *slotPool) destroyWorker(w *Worker) {
	w.Destroy()
}

// Close closes the pool and destroys every idle worker (borrowed ones are destroyed on Release).
func (p *slotPool) Close() {
	p.closeOnce.Do(func() {
		close(p.closed)

		if p.idle == nil {
			return
		}
		for {
			select {
			case w := <-p.idle:
				w.Destroy()
			default:
				return
			}
		}
	})
}
