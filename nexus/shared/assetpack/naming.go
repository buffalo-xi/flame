package assetpack

import (
	"fmt"
	"strings"

	"flame/nexus/shared/paths"
)

// MaxNameLen is the name length ceiling (the same as paths.ValidName, and effectively the ceiling
// for a directory name).
const MaxNameLen = 64

// ValidateName validates an entity name at listing / install time.
//
// It applies the same rule as paths.ValidName (letters, digits and dashes, no leading dash, ≤64):
// the name ends up as a directory name (tenants/<schema>/skills/<name>), so a loose name leaves an
// injection surface for the storage layer.
func ValidateName(name string) error {
	if !paths.ValidName(name) {
		return fmt.Errorf("the name %q is not valid: only letters, digits and dashes, no leading dash, at most %d characters",
			name, MaxNameLen)
	}
	return nil
}

// SuggestName suggests a non-conflicting name within the taken set: name-2, name-3 …
//
// A conflict is **never renamed silently**: installing as `name-2` behind the user's back makes them
// believe they installed the original, after which grants, bindings and troubleshooting all
// disagree. The right behaviour is for the API to answer 409 with this suggestion, and the user to
// retry with name= after confirming.
func SuggestName(base string, taken []string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "asset"
	}

	set := make(map[string]struct{}, len(taken))
	for _, t := range taken {
		if t = strings.TrimSpace(t); t != "" {
			set[strings.ToLower(t)] = struct{}{}
		}
	}
	if _, clash := set[strings.ToLower(base)]; !clash {
		return base
	}

	// taken is a finite set, so a free slot exists as soon as i passes len(set)+1; the upper bound
	// is pure defence.
	for i := 2; i <= len(set)+2; i++ {
		suffix := fmt.Sprintf("-%d", i)
		stem := base
		if len(stem)+len(suffix) > MaxNameLen {
			stem = stem[:MaxNameLen-len(suffix)]
		}
		cand := stem + suffix
		if _, clash := set[strings.ToLower(cand)]; !clash {
			return cand
		}
	}
	return base // unreachable in practice
}
