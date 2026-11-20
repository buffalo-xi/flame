package sessions

import (
	"errors"
	"testing"
)

// ParseID is the only way a client-supplied sid may reach SQL. What it blocks is exactly PostgreSQL's 22P02 (invalid
// input syntax for type uuid).
func TestParseID(t *testing.T) {
	const valid = "a0000000-0000-4000-8000-000000000010"

	cases := []struct {
		name string
		in   string
		want string
		err  error
	}{
		{"valid", valid, valid, nil},
		{"surrounding whitespace", "  " + valid + "\t", valid, nil},
		{"empty string", "", "", ErrSessionRequired},
		{"whitespace only", "   ", "", ErrSessionRequired},
		{"not a UUID", "not-a-uuid", "", ErrSessionInvalid},
		{"one segment short", "a0000000-0000-4000-8000", "", ErrSessionInvalid},
		{"path-like input", "../../etc/passwd", "", ErrSessionInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseID(c.in)
			if c.err != nil {
				if !errors.Is(err, c.err) {
					t.Fatalf("err = %v, want %v", err, c.err)
				}
				if got != "" {
					t.Errorf("a failure must not return an id, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// The three sentinel errors must stay distinguishable: the entry layer maps them onto different protocol error codes
// (InvalidParams vs InternalError), while "does not exist" and "not yours" share one.
func TestSessionErrorsAreDistinct(t *testing.T) {
	all := []error{ErrSessionRequired, ErrSessionInvalid, ErrSessionForbidden}
	for i := range all {
		for j := range all {
			if i != j && errors.Is(all[i], all[j]) {
				t.Errorf("%v and %v should not be the same error", all[i], all[j])
			}
		}
	}
}
