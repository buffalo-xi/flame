package globalvars

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"flame/nexus/shared/auth"
	varsvc "flame/nexus/shared/globalvars"
	globalvarsrepo "flame/nexus/shared/repository/globalvars"
	"flame/tools/xhttp"
	"flame/tools/xsql"

	"github.com/gin-gonic/gin"
)

// ============================================================================
// Global variables ({schema}.global_vars)
//
// The scope is "global within this tenant": every agent / flow / skill may
// reference them, and they never cross tenants. Together with flows.env_vars
// (environment variables, one flow) and flows.flow_vars (flow variables) they
// belong to the <scope>_vars family; the global scope is simply "global".
//
// Security rules:
//   - a kind=secret value is stored encrypted (the repository encrypts and
//     decrypts, see shared/secureval) and is always masked on the way out
//     (has_value is returned, never the plaintext);
//   - an exported asset pack never carries values - it holds reference names
//     only, so a secret cannot leave by construction.
// ============================================================================

var store = globalvarsrepo.NewRepository()

// nameRe is the character set for variable names: the same one as entity names
// (letters, digits and underscores, not starting with a digit). The name ends
// up in a node's source_key and in the ${global_vars.<name>} form used by
// documents, so a dot, a space or a non-ASCII character would break template
// parsing or document substitution.
var nameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// secretMask is the masking placeholder: any value returns this string, so its
// length cannot hint at the content.
const secretMask = "••••••"

// toJSON is the single exit point for responses: a secret is never returned in
// plaintext, only "has a value" is reported.
func toJSON(v *globalvarsrepo.GlobalVar) gin.H {
	if v == nil {
		return gin.H{}
	}
	out := gin.H{
		"name":        v.Name,
		"kind":        v.Kind,
		"description": v.Description,
		"updated_at":  v.UpdatedAt,
		"has_value":   v.Value != "",
	}
	if v.Kind == globalvarsrepo.KindSecret {
		// With a value the mask is returned, without one it stays empty - that is how
		// the frontend tells "already set" from "not set yet".
		if v.Value != "" {
			out["value"] = secretMask
		} else {
			out["value"] = ""
		}
	} else {
		out["value"] = v.Value
	}
	return out
}

// ListGlobalVars lists every global variable of the tenant, by name ascending.
func ListGlobalVars(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}
	list, err := store.List(info.Schema)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to read global variables: "+err.Error(), "")
		return
	}
	items := make([]gin.H, 0, len(list))
	for i := range list {
		items = append(items, toJSON(&list[i]))
	}
	xhttp.OkJSON(c, "Success", gin.H{"items": items}, "")
}

// saveRequest creates or overwrites a variable.
//
// value is a *string: for the secret kind "absent or empty" means **keep the
// current value** (the plaintext is never echoed back, so a user who does not
// change the password necessarily submits an empty string).
type saveRequest struct {
	Name        string  `json:"name"`
	Value       *string `json:"value"`
	Kind        string  `json:"kind"`
	Description string  `json:"description"`
}

// UpsertGlobalVar creates or overwrites a global variable by name.
func UpsertGlobalVar(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}

	var req saveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		xhttp.ErrCode(c, "common.invalid_params", nil, "invalid request body: "+err.Error(), "")
		return
	}
	name := strings.TrimSpace(req.Name)
	if !nameRe.MatchString(name) {
		xhttp.ErrCode(c, "globalvar.invalid_name", nil, "variable names may only contain letters, digits and underscores, and must not start with a digit (max 64)", "")
		return
	}

	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = globalvarsrepo.KindText
	}
	switch kind {
	case globalvarsrepo.KindText, globalvarsrepo.KindSecret,
		globalvarsrepo.KindNumber, globalvarsrepo.KindBool:
	default:
		xhttp.ErrCode(c, "globalvar.invalid_type", nil, "kind must be text / secret / number / bool", "")
		return
	}

	prev, err := store.Get(info.Schema, name)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to read global variables: "+err.Error(), "")
		return
	}

	value := ""
	if req.Value != nil {
		value = *req.Value
	}
	// secret + empty value + already exists: keep the current value (the plaintext
	// is never echoed back, so an empty submission means "unchanged").
	if kind == globalvarsrepo.KindSecret && value == "" && prev != nil && prev.Kind == globalvarsrepo.KindSecret {
		value = prev.Value
	}

	if err := validateValue(name, kind, value); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, err.Error(), "")
		return
	}

	row := &globalvarsrepo.GlobalVar{
		Name:        name,
		Value:       value,
		Kind:        kind,
		Description: strings.TrimSpace(req.Description),
		UpdatedBy:   xsql.UUID(info.ID),
	}
	if err := store.Upsert(info.Schema, row); err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to save the global variable: "+err.Error(), "")
		return
	}

	// Invalidate after the write: this instance takes effect at once, other
	// instances expire naturally on the 5s TTL.
	varsvc.Invalidate(info.Schema)

	xhttp.OkJSON(c, "Success", gin.H{"variable": toJSON(row)}, "")
}

// DeleteGlobalVar deletes a global variable.
//
// By default it refuses while the variable is still referenced: once deleted,
// the nodes and documents that reference it only fail at run time, often on a
// call much later. The moment of deletion is the only chance to say what will
// break, so the references are scanned first (ScanUsage) and the delete is
// refused with the locations listed.
//
// force=1 actually deletes: the escape hatch exists because "the variable
// itself was created by mistake, the references are leftovers" does happen, but
// it has to be a second press after reading the list.
func DeleteGlobalVar(c *gin.Context) {
	info, ok := authInfo(c)
	if !ok {
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	if name == "" {
		xhttp.ErrCode(c, "globalvar.name_required", nil, "variable name is required", "")
		return
	}

	// No hits does not mean no references: on error the delete is refused,
	// "unknown" is never treated as "safe".
	if c.Query("force") != "1" {
		hits, uerr := ScanUsage(info.Schema, name)
		if uerr != nil {
			xhttp.ErrCode(c, "globalvar.in_use", nil, "cannot determine the reference surface, deletion refused (pass force=1 to delete anyway): "+uerr.Error(), "")
			return
		}
		if len(hits) > 0 {
			xhttp.ErrCode(c, "globalvar.referenced", gin.H{"name": name, "count": len(hits)},
				fmt.Sprintf("%q is still referenced in %d place(s): %s. Those will fail at run time once it is deleted; pass force=1 to delete anyway",
					name, len(hits), describeHits(hits)), "")
			return
		}
	}

	removed, err := store.Delete(info.Schema, name)
	if err != nil {
		xhttp.ErrCode(c, "common.internal", nil, "failed to delete the global variable: "+err.Error(), "")
		return
	}
	if !removed {
		xhttp.ErrCode(c, "globalvar.not_found", gin.H{"name": name}, "global variable not found: "+name, "")
		return
	}

	varsvc.Invalidate(info.Schema)
	xhttp.OkJSON(c, "Success", gin.H{"deleted": name}, "")
}

// validateValue rejects a type mismatch before it is stored: discovering at run
// time that a variable declared as number holds "abc" costs far more to debug
// than one error at save time.
func validateValue(name, kind, value string) error {
	switch kind {
	case globalvarsrepo.KindNumber:
		if strings.TrimSpace(value) == "" {
			return nil // empty means 0, matching how resolve reads it
		}
		if !isNumber(strings.TrimSpace(value)) {
			return fmt.Errorf("variable %s is declared as number but %q is not a number", name, value)
		}
	case globalvarsrepo.KindBool:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "", "true", "false", "0", "1", "yes", "no", "on", "off":
		default:
			return fmt.Errorf("variable %s is declared as bool but %q is not a boolean (true/false/1/0/yes/no/on/off)", name, value)
		}
	}
	return nil
}

func isNumber(s string) bool {
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		return true
	}
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// authInfo returns the signed-in tenant (injected by the admin middleware).
func authInfo(c *gin.Context) (*auth.AuthInfo, bool) {
	value, exists := c.Get("authInfo")
	if !exists {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication info not found", "")
		return nil, false
	}
	info, ok := value.(*auth.AuthInfo)
	if !ok || info == nil {
		xhttp.ErrCode(c, "common.internal", nil, "Authentication information not found", "")
		return nil, false
	}
	return info, true
}
