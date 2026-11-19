package conv

import (
	"testing"
)

type testStruct struct {
	Name  string
	Age   int
	Email string
}

func TestGetPropertyAsString(t *testing.T) {
	// Test with struct
	structObj := testStruct{
		Name:  "John",
		Age:   30,
		Email: "john@example.com",
	}

	// Test existing property
	name, _, err := GetPropertyAsString(structObj, "Name")
	if err != nil || name != "John" {
		t.Errorf("Expected 'John', got '%s' with error: %v", name, err)
	}

	// Test non-existent property
	_, _, err = GetPropertyAsString(structObj, "NonExistent")
	if err == nil {
		t.Error("Expected error for non-existent property")
	}

	// Test with map
	mapObj := map[string]interface{}{
		"name":  "John",
		"age":   30,
		"email": "john@example.com",
	}

	// Test existing property in map
	name, _, err = GetPropertyAsString(mapObj, "name")
	if err != nil || name != "John" {
		t.Errorf("Expected 'John', got '%s' with error: %v", name, err)
	}

	// Test non-existent property in map
	_, _, err = GetPropertyAsString(mapObj, "non_existent")
	if err == nil {
		t.Error("Expected error for non-existent property in map")
	}

	// Test with nil object
	var nilObj interface{}
	_, _, err = GetPropertyAsString(nilObj, "Name")
	if err == nil {
		t.Error("Expected error for nil object")
	}

	// Test with pointer to struct
	structPtr := &testStruct{
		Name:  "John",
		Age:   30,
		Email: "john@example.com",
	}

	name, _, err = GetPropertyAsString(structPtr, "Name")
	if err != nil || name != "John" {
		t.Errorf("Expected 'John', got '%s' with error: %v", name, err)
	}

	// Test with nil pointer
	var nilPtr *testStruct
	_, _, err = GetPropertyAsString(nilPtr, "Name")
	if err == nil {
		t.Error("Expected error for nil pointer")
	}
}

func TestToString(t *testing.T) {
	cases := []struct {
		in   interface{}
		want string
	}{
		{nil, ""},
		{"abc", "abc"},
		{true, "true"},
		{false, "false"},
		{42, "42"},
		{int64(42), "42"},
		{uint8(7), "7"},
		{1.5, "1.5"},
		{[]int{1}, ""}, // an unsupported type returns an empty string
	}
	for _, c := range cases {
		if got := ToString(c.in); got != c.want {
			t.Errorf("ToString(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestToBool(t *testing.T) {
	cases := []struct {
		in   interface{}
		want bool
	}{
		{nil, false},
		{true, true},
		{"true", true},
		{"1", true},
		{"on", true},
		{"yes", true},
		{"false", false},
		{"no", false},
		{1, true},
		{0, false},
		{2.5, true},
	}
	for _, c := range cases {
		if got := ToBool(c.in); got != c.want {
			t.Errorf("ToBool(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
