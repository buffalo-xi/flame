package globalvars

import (
	"fmt"

	repo "flame/nexus/shared/repository/globalvars"
)

// ValidateDocRefs is the pre-save validation: a global variable referenced by a document (AGENT.md / SKILL.md)
// must not be a secret.
//
// Only that one rule is enforced, not "it references a variable that does not exist yet":
//   - a secret reaches the model context, session_records and SSE — once written into a prompt it is a leak,
//     and the author usually believes "I referenced the name, not the value";
//   - a nonexistent variable is not blocked: writing the document before creating the variable is a normal
//     order, and at runtime a missing value fails hard with the variable name in the message rather than
//     running wrongly in silence.
//
// An empty schema (platform level) passes outright: without a tenant there are no global variables to look up.
func ValidateDocRefs(schema, text string) error {
	if schema == "" || text == "" {
		return nil
	}
	refs := DocRefs(text)
	if len(refs) == 0 {
		return nil
	}

	// Query the database rather than going through Resolve: Resolve has a 5s cache, and a variable just changed
	// to secret may not be in it yet, while this check should err on the strict side — letting one through
	// before the cache expires is a real leak.
	store := repo.NewRepository()
	for _, name := range refs {
		v, err := store.Get(schema, name)
		if err != nil {
			return fmt.Errorf("failed to validate global variable %s: %w", name, err)
		}
		if v == nil {
			continue // not created yet: writing the document first is allowed
		}
		if v.Kind == repo.KindSecret {
			return fmt.Errorf(
				"a secret global variable cannot be referenced: %s reaches the model context and the run records (${global_vars.%s}). "+
					"Use secrets only in connection / tool configuration, or switch to a text variable", name, name)
		}
	}
	return nil
}
