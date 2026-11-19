package handler

import "testing"

// TestCheckQuotaSystemValuesNamesTheField: the helper has to hand back the offending field name, because the HTTP layer
// renders quota.invalid_value from params.field. Losing the name would show a literal "{field}" to whoever is editing quotas.
func TestCheckQuotaSystemValuesNamesTheField(t *testing.T) {
	zero, minusOne, minusTwo := 0, -1, -2

	field, err := checkQuotaSystemValues([]intField{
		{"max_agents", &zero},
		{"max_flows", &minusTwo},
		{"max_skills", &minusTwo},
	})
	if err == nil || field != "max_flows" {
		t.Fatalf("field = %q, err = %v; want max_flows and an error", field, err)
	}

	if field, err := checkQuotaSystemValues([]intField{{"max_agents", &minusOne}, {"max_skills", nil}}); err != nil {
		t.Fatalf("field = %q, err = %v; want no error for -1 (unlimited) and for a field that was not sent", field, err)
	}
	if field, err := checkQuotaSystemValues([]intField{{"max_agents", &zero}}); err != nil {
		t.Fatalf("field = %q, err = %v; want no error for 0 (nothing allowed)", field, err)
	}
}
