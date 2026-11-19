package globalvars

import (
	"fmt"
	"regexp"
	"strings"
)

// The reference form inside a document: ${global_vars.NAME}
//
// Why documents do not use Go templates ({{.global_vars.X}}): AGENT.md / SKILL.md are documents, and {{ }} is
// far too common in markdown and code samples — executing one as a template would be a disaster ({{range}} and
// the like). Here it is a single namespaced regex substitution, with a bounded scope.
var docRefRe = regexp.MustCompile(`\$\{global_vars\.([A-Za-z_][A-Za-z0-9_]*)\}`)

// Expand replaces ${global_vars.NAME} in a document with its value.
//
// The semantics that matter:
//   - no reference -> returned as is, no database query (the vast majority of documents hold no reference, so
//     do not pay for a Resolve);
//   - an unknown variable -> an error. Neither the ${...} literal is kept (the model would read it) nor an
//     empty string substituted (a missing configuration would look like "this is how the user configured it");
//   - a reference to a kind=secret variable -> an error. ValidateDocRefs blocks it on save, but a document
//     installed from an asset package did not go through that check, so it has to be blocked again here at
//     runtime: once expanded it is ordinary text and there is no telling which characters came from a secret,
//     while it reaches the system prompt, session_records and SSE;
//   - an empty schema (platform level, unit tests) -> returned as is: it belongs to no tenant, so there is no
//     value to resolve.
func Expand(schema, text string) (string, error) {
	if text == "" || schema == "" || !strings.Contains(text, "${global_vars.") {
		return text, nil
	}

	vars, err := Resolve(schema)
	if err != nil {
		return "", err
	}
	secrets, err := SecretNames(schema)
	if err != nil {
		return "", err
	}

	var missing, blocked []string
	out := docRefRe.ReplaceAllStringFunc(text, func(m string) string {
		name := docRefRe.FindStringSubmatch(m)[1]
		value, ok := vars[name]
		if !ok {
			missing = append(missing, name)
			return m
		}
		if secrets[name] {
			blocked = append(blocked, name)
			return m
		}
		return fmt.Sprint(value)
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("the document references global variables that do not exist: %s (create them first under Settings → Global variables)",
			strings.Join(missing, ", "))
	}
	if len(blocked) > 0 {
		return "", fmt.Errorf("a document may not reference secret global variables: %s (the value would reach the model context and the run records)",
			strings.Join(blocked, ", "))
	}
	return out, nil
}

// DocRefs returns the global variable names a document references (deduplicated, in order of appearance).
//
// Used by the pre-save validation: this list is enough to decide "it references a variable that does not exist"
// and "it references a secret".
func DocRefs(text string) []string {
	if text == "" {
		return nil
	}
	matches := docRefRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if _, ok := seen[m[1]]; ok {
			continue
		}
		seen[m[1]] = struct{}{}
		out = append(out, m[1])
	}
	return out
}
