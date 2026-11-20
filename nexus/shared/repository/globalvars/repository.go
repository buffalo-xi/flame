package globalvars

import (
	"flame/tools/xsql"
)

// Variable kinds. The frontend dropdown and the template-injection type conversion share one set of values;
// do not write another set of string literals in the handler / resolve.
const (
	KindText   = "text"
	KindSecret = "secret"
	KindNumber = "number"
	KindBool   = "bool"
)

// GlobalVar is one global variable row.
//
// Value is **always plaintext in the process**; encryption happens only at the moment of writing: a kind=secret value is
// encrypted by the repository into `enc:v1:...` (see shared/secureval) and decrypted again on read. In other words,
// "encryption at rest" is the repository's job, and the callers (handler / resolve) always get plaintext — they neither
// need to nor should handle ciphertext themselves.
//
// Masking in the API response is a different matter and happens in admin/globalvars; an exported asset pack never carries
// the value — the pack only holds the reference name.
type GlobalVar struct {
	Name        string    `json:"name"`
	Value       string    `json:"value"`
	Kind        string    `json:"kind"`
	Description string    `json:"description"`
	UpdatedBy   xsql.UUID `json:"updated_by"`
	UpdatedAt   xsql.Time `json:"updated_at"`
}
