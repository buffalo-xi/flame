// Package browserinfo keeps the capability snapshot of the standalone browser pool (browserd).
//
// It is a separate package from sandboxinfo on purpose: the two describe different services and answer different
// questions ("can this sandbox run code" vs "can this deployment run a browser"), and merging them would make "sandboxd
// did not report a browser" indistinguishable from "browserd is down". The browser is not part of the sandbox any more —
// it has its own capacity limits, its own isolation boundary and its own health.
package browserinfo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"flame/tools/xlog"
)

// MaxBodyBytes caps how much of the response body is read: this is internal diagnostics, not payload.
const MaxBodyBytes = 1 << 20

// FetchTimeout is the ceiling for one fetch. browserd is an internal service, so a generous value is fine.
const FetchTimeout = 10 * time.Second

// Info is browserd's capability snapshot (the body of GET /v1/capabilities).
//
// Available is the pool's **measured** conclusion (it runs the driver's self-test), not a config statement:
//
//	available=false + error="probing browser version" — still starting up
//	available=false + error="..."                    — the driver or Chromium is missing/broken
//	available=true                                   — a browser can be launched
type Info struct {
	Available       bool   `json:"available"`
	Engine          string `json:"engine,omitempty"`
	Version         string `json:"version,omitempty"`
	HeadedAvailable bool   `json:"headed_available"`
	HeadlessDefault bool   `json:"headless_default"`

	// Capacity, carried so the tool can explain "every worker is busy" instead of "the browser is broken".
	Workers  int `json:"workers"`
	Sessions int `json:"sessions"`
	Busy     int `json:"busy"`
	Queue    int `json:"queue"`

	// Error is the reason the pool reported itself unavailable.
	Error string `json:"error,omitempty"`

	// FetchedAt is when the last **successful** fetch happened.
	FetchedAt time.Time `json:"-"`
	// Err is why the last fetch failed. When non-empty, the fields above are the last successful snapshot (possibly
	// stale) rather than "no capability" — the two mean different things to the agent.
	Err string `json:"-"`
}

var (
	current    atomic.Pointer[Info]
	configured atomic.Bool
)

// Set injects a capability snapshot.
func Set(i Info) {
	current.Store(&i)
	configured.Store(true)
}

// Get returns the current snapshot; a zero value when nothing was ever injected (callers tell the two apart with
// Configured).
func Get() Info {
	if p := current.Load(); p != nil {
		return *p
	}
	return Info{}
}

// Configured reports whether a snapshot was ever injected. Before that, browser.url was not configured for this
// deployment — the tool must say "no browser here" rather than guess.
func Configured() bool { return configured.Load() }

// Fetch pulls one capability snapshot from browserd.
func Fetch(ctx context.Context, endpoint, token string) (Info, error) {
	if endpoint == "" {
		return Info{}, errors.New("browserinfo: capabilities endpoint is empty")
	}
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Info{}, fmt.Errorf("browserinfo: build request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Info{}, fmt.Errorf("browserinfo: get %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Info{}, fmt.Errorf("browserinfo: get %s: http %d", endpoint, resp.StatusCode)
	}

	var payload Info
	if derr := json.NewDecoder(io.LimitReader(resp.Body, MaxBodyBytes)).Decode(&payload); derr != nil {
		return Info{}, fmt.Errorf("browserinfo: decode %s: %w", endpoint, derr)
	}
	payload.FetchedAt = time.Now()
	return payload, nil
}

// StartRefresher starts the background refresh: fetch immediately, then once per interval until ctx ends.
//
// A transport failure **does not overwrite** the last successful data and only records Err — a possibly stale but real
// snapshot beats "unknown". "available=false" is different: that is a successful answer from a live service, and it is
// stored as such.
func StartRefresher(ctx context.Context, endpoint, token string, interval time.Duration) {
	if endpoint == "" {
		xlog.Error("browserinfo: capabilities endpoint is empty - the browser tool will report itself unconfigured")
		return
	}
	if interval <= 0 {
		interval = time.Minute
	}

	go func() {
		failing := false
		lastAvailable := false
		for {
			info, err := Fetch(ctx, endpoint, token)
			if err != nil {
				last := Get()
				last.Err = err.Error()
				Set(last)
				if !failing {
					xlog.Error("browserinfo: refresh failed: %v", err)
					failing = true
				}
			} else {
				Set(info)
				if failing {
					xlog.Info("browserinfo: refresh recovered (available=%v version=%s)", info.Available, info.Version)
					failing = false
				}
				// Log transitions only: the probe already retries internally, so repeating the same state here would
				// just be noise.
				if info.Available != lastAvailable {
					if info.Available {
						xlog.Info("browserinfo: browser available (%s, headed_available=%v)", info.Version, info.HeadedAvailable)
					} else {
						xlog.Error("browserinfo: browser unavailable: %s", info.Error)
					}
					lastAvailable = info.Available
				}
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
		}
	}()
}
