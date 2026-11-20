package globalvars

import "testing"

// With no tenant context (platform level / a unit test) the input is returned as is: it belongs to no tenant, so there is
// no value to resolve.
func TestExpandWithoutSchema(t *testing.T) {
	in := "数据库地址是 ${global_vars.DB_HOST}"
	out, err := Expand("", in)
	if err != nil {
		t.Fatalf("an empty schema should not error: %v", err)
	}
	if out != in {
		t.Fatalf("an empty schema should return the input unchanged, got %q", out)
	}
}

// With no references no query runs (and nothing errors): most documents contain no ${global_vars.X} at all.
func TestExpandWithoutRefs(t *testing.T) {
	out, err := Expand("t0", "# 标题\n没有任何引用")
	if err != nil {
		t.Fatalf("no references should not error: %v", err)
	}
	if out != "# 标题\n没有任何引用" {
		t.Fatalf("the content was modified: %q", out)
	}
}

func TestDocRefs(t *testing.T) {
	text := "用 ${global_vars.DB_HOST} 和 ${global_vars.DB_HOST}，再来 ${global_vars.REGION}"
	got := DocRefs(text)
	want := []string{"DB_HOST", "REGION"}
	if len(got) != len(want) {
		t.Fatalf("DocRefs = %v, want %v (duplicates must be removed)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DocRefs = %v, want %v (order of appearance must be kept)", got, want)
		}
	}
}

// A variable name must fit the character set: a dotted or bracketed form is not a reference (so sample code is not
// rewritten by accident).
func TestDocRefsIgnoresInvalidNames(t *testing.T) {
	if got := DocRefs("${global_vars.a.b} ${global_vars.中文}"); len(got) != 0 {
		t.Fatalf("an invalid variable name should not be recognised, got %v", got)
	}
}
