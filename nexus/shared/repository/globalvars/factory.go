package globalvars

// NewRepository creates the global variable storage instance.
func NewRepository() Repository {
	return &PostgresRepository{}
}

// Repository is the global variable storage interface.
//
// No paging: the volume is "a dozen to a few dozen" variables, so paging is unnecessary,
// and adding it would only give the caller one more layer of page-flipping logic. Revisit it when there really are
// hundreds.
type Repository interface {
	// List lists all variables of a tenant (by name ascending).
	List(schema string) ([]GlobalVar, error)
	// Get fetches one; missing returns (nil, nil), leaving it to the caller to decide between an error and a create.
	Get(schema, name string) (*GlobalVar, error)
	// Upsert writes or overwrites by name (created_by is only written on the first create and is left alone afterwards).
	Upsert(schema string, v *GlobalVar) error
	// Delete deletes one, reporting whether it was really deleted (missing returns false, not an error).
	Delete(schema, name string) (bool, error)
}
