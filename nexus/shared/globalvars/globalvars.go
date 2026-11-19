package globalvars

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	repo "flame/nexus/shared/repository/globalvars"
	"flame/tools/xlog"
)

// The value layer: it turns the rows of <schema>.global_vars into a map that can be injected into a template
// directly.
//
// How it divides the work with shared/repository/globalvars: that one owns the SQL, this one owns the cache and
// the type conversion. A caller (node execution, environment variable resolution) should only see Resolve and
// never query the database itself.

// cacheTTL is how long the in-process cache lives.
//
// Why no Redis cache: the values live in the same table as secrets, so writing them into Redis means copying
// the plaintext onto another access surface; and the read frequency is "one snapshot per run", which a 5
// second TTL is enough to cut almost all duplicates of. Across instances a change takes at most 5 seconds to
// take effect, the same order of magnitude as a Redis scheme.
const cacheTTL = 5 * time.Second

type entry struct {
	values map[string]any
	// secrets is cached together with values: the call sites that decide "is this reference a secret" sit on
	// the hot path of node execution (before every prompt render), and a separate database query is not worth
	// it.
	secrets map[string]bool
	at      time.Time
}

var cache = struct {
	sync.RWMutex
	m map[string]entry
}{m: make(map[string]entry)}

// Resolve returns a tenant's global variables (converted to native types by kind: number→float64, bool→bool).
//
// A failure always returns an error and **never degrades to an empty map** — an empty map would make "it runs
// even when unconfigured" the production reality: once a node input resolves to nil, the script or request
// carries on with an empty value, and that kind of error is far more expensive to trace than a plain failure.
func Resolve(schema string) (map[string]any, error) {
	e, err := snapshot(schema)
	if err != nil {
		return nil, err
	}
	return copyValues(e.values), nil
}

// SecretNames returns the set of variable names with kind=secret in this tenant.
//
// Its purpose is "blocking the places where they must not appear": once a secret value shows up in a prompt, a
// session record or SSE it is a leak, and those channels receive the rendered string, which no longer says
// which part came from a secret — so the decision has to happen before rendering.
func SecretNames(schema string) (map[string]bool, error) {
	e, err := snapshot(schema)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(e.secrets))
	for name := range e.secrets {
		out[name] = true
	}
	return out, nil
}

// snapshot takes the cache entry, rebuilding it from the source when it is stale or missing.
func snapshot(schema string) (entry, error) {
	if schema == "" {
		return entry{}, fmt.Errorf("global variables: the tenant schema is empty (the tenant is not initialised)")
	}

	cache.RLock()
	e, ok := cache.m[schema]
	cache.RUnlock()
	if ok && time.Since(e.at) < cacheTTL {
		return e, nil
	}

	rows, err := repo.NewRepository().List(schema)
	if err != nil {
		// A failed read is not cached: the next request retries immediately rather than caching "broken" for 5
		// seconds
		xlog.Error("failed to read global variables schema=%s: %v", schema, err)
		return entry{}, fmt.Errorf("failed to read global variables: %w", err)
	}

	values := make(map[string]any, len(rows))
	secrets := make(map[string]bool)
	for _, row := range rows {
		v, cerr := convert(row)
		if cerr != nil {
			return entry{}, cerr
		}
		values[row.Name] = v
		if row.Kind == repo.KindSecret {
			secrets[row.Name] = true
		}
	}

	e = entry{values: values, secrets: secrets, at: time.Now()}
	cache.Lock()
	cache.m[schema] = e
	cache.Unlock()
	return e, nil
}

// Invalidate expires this instance's cache immediately (called after a variable is written; other instances
// rely on the TTL).
func Invalidate(schema string) {
	cache.Lock()
	delete(cache.m, schema)
	cache.Unlock()
}

// copyValues is a shallow copy: the values are all scalars, so shallow is enough; returning a reference to the
// internal map would let a caller (even by accident) dirty the cache.
func copyValues(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// convert turns a value into its native type by kind, so a template can compare and compute with it directly
// instead of every variable being a string.
func convert(row repo.GlobalVar) (any, error) {
	switch row.Kind {
	case repo.KindNumber:
		if strings.TrimSpace(row.Value) == "" {
			return float64(0), nil
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(row.Value), 10, 64); err == nil {
			return float64(n), nil
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(row.Value), 64)
		if err != nil {
			return nil, fmt.Errorf("global variable %s is declared as number but its value %q is not a number", row.Name, row.Value)
		}
		return f, nil
	case repo.KindBool:
		// Lenient parsing: besides true/false the config also accepts 1/0, yes/no and on/off
		switch strings.ToLower(strings.TrimSpace(row.Value)) {
		case "", "false", "0", "no", "off":
			return false, nil
		case "true", "1", "yes", "on":
			return true, nil
		default:
			return nil, fmt.Errorf("global variable %s is declared as bool but its value %q is not a boolean", row.Name, row.Value)
		}
	case repo.KindSecret, repo.KindText:
		return row.Value, nil
	default:
		return nil, fmt.Errorf("the type %q of global variable %s is not supported (only text / secret / number / bool)", row.Kind, row.Name)
	}
}
