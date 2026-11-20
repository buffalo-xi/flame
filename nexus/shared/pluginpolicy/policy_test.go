package pluginpolicy

import "testing"

// The default must be open (a product decision of 2026-11-02): the plugin itself is trusted code the platform put on
// disk, the credentials and the cost sit on the tenant side (the keys in {schema}.plugins.configs are the tenant's own),
// and enabling tenants one by one is pure operational overhead. To tighten it, write one is_allowed=false row.
//
// Note: the **zero value** of Policy is still Allowed=false — the default semantics are expressed by Default() alone, and
// whoever reads the table without a row must call Default() explicitly rather than rely on the zero value. This also
// keeps anyone from later using the zero value of a failed `map[name]` lookup as the policy and silently turning the
// default back into "refused" (or the other way round).
func TestDefaultAllows(t *testing.T) {
	p := Default()
	if !p.Allowed {
		t.Fatalf("the default policy should be \"open\", got Allowed=false")
	}
	if p.Blocked {
		t.Fatalf("the default should not be blocked by the global gate")
	}

	var zero Policy
	if zero.Allowed {
		t.Fatalf("the zero value of Policy should stay false (the default semantics live in Default() only)")
	}
}
