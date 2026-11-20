package storage

import (
	"context"
	"time"
)

// LastActivity estimates the "last activity time" of a directory, for sweep logic that cleans up by age.
//
// It prefers the directory's own ModTime; an S3 "directory" has no such property (Entry.ModTime is the zero value, see the
// Entry comment), in which case it falls back to the newest ModTime among the children — on object storage files do carry
// LastModified. When neither is available it returns false, and the caller must therefore **not delete**, rather than
// treating the zero value as 1970 and sweeping the whole tree away as expired data.
//
// self must be the directory's own entry as returned by the caller's List(parent), saving one Stat.
func LastActivity(ctx context.Context, st Storage, dir string, self Entry) (time.Time, bool) {
	if !self.ModTime.IsZero() {
		return self.ModTime, true
	}
	children, err := st.List(ctx, dir)
	if err != nil {
		return time.Time{}, false
	}
	var newest time.Time
	for _, c := range children {
		if c.ModTime.After(newest) {
			newest = c.ModTime
		}
	}
	if newest.IsZero() {
		return time.Time{}, false
	}
	return newest, true
}
