package xsql

import (
	"encoding/json"
	"testing"
	"time"
)

// TestScanCoverage pins the two promises of every Scan implementation:
// a database NULL becomes the Go zero value, and both the PostgreSQL and the
// MySQL representation of the same column are accepted.
func TestScanCoverage(t *testing.T) {
	t.Run("String", func(t *testing.T) {
		s := String("x")
		if err := s.Scan(nil); err != nil || s != "" {
			t.Fatalf("NULL: %v %q", err, s)
		}
		if err := s.Scan("abc"); err != nil || s != "abc" {
			t.Fatalf("string: %v %q", err, s)
		}
		if err := s.Scan([]byte("abc")); err != nil || s != "abc" {
			t.Fatalf("[]byte: %v %q", err, s)
		}
		if err := s.Scan(int64(1)); err == nil {
			t.Fatal("int64 should be rejected")
		}
	})

	t.Run("Bool", func(t *testing.T) {
		cases := []struct {
			src  interface{}
			want bool
		}{
			{nil, false},
			{true, true},
			{int64(1), true},
			{int64(0), false},
			{"1", true},
			{" 0 ", false},
			{[]byte("TRUE"), true},
			{[]byte("off"), false},
		}
		for _, c := range cases {
			var b Bool
			if err := b.Scan(c.src); err != nil || b.Bool() != c.want {
				t.Errorf("%T(%v): %v %v, want %v", c.src, c.src, err, b, c.want)
			}
		}
		var b Bool
		if err := b.Scan([]byte("maybe")); err == nil {
			t.Error("invalid literal should be rejected")
		}
	})

	t.Run("Int64", func(t *testing.T) {
		var i Int64
		for _, c := range []struct {
			src  interface{}
			want int64
		}{{nil, 0}, {int64(5), 5}, {7, 7}, {" 8 ", 8}, {[]byte("9"), 9}} {
			if err := i.Scan(c.src); err != nil || i.Int64() != c.want {
				t.Errorf("%T(%v): %v %v, want %v", c.src, c.src, err, i, c.want)
			}
		}
		if err := i.Scan([]byte("x")); err == nil {
			t.Error("invalid literal should be rejected")
		}
	})

	t.Run("Float64", func(t *testing.T) {
		var f Float64
		for _, c := range []struct {
			src  interface{}
			want float64
		}{{nil, 0}, {float64(1.5), 1.5}, {int64(2), 2}, {" 3.25 ", 3.25}, {[]byte("4.5"), 4.5}} {
			if err := f.Scan(c.src); err != nil || f.Float64() != c.want {
				t.Errorf("%T(%v): %v %v, want %v", c.src, c.src, err, f, c.want)
			}
		}
		if err := f.Scan([]byte("x")); err == nil {
			t.Error("invalid literal should be rejected")
		}
	})

	t.Run("Int", func(t *testing.T) {
		var i Int
		if err := i.Scan(nil); err != nil || i != 0 {
			t.Fatalf("NULL: %v %d", err, i)
		}
		if err := i.Scan([]byte("42")); err != nil || i.Int() != 42 {
			t.Fatalf("[]byte: %v %d", err, i)
		}
	})

	t.Run("UUID", func(t *testing.T) {
		const id = "8f14e45f-ea0c-4a1e-9e2f-7c1b0f3d5a61"
		var u UUID
		if err := u.Scan(nil); err != nil || u != "" {
			t.Fatalf("NULL: %v %q", err, u)
		}
		if err := u.Scan([]byte(id)); err != nil || u.String() != id {
			t.Fatalf("[]byte: %v %q", err, u)
		}
		if err := u.Scan(id); err != nil || u.String() != id {
			t.Fatalf("string: %v %q", err, u)
		}
	})
}

// TestTime pins the zero-value contract: the database NULL, the zero time and
// the JSON payload all line up, and the textual MySQL forms parse as well.
func TestTime(t *testing.T) {
	want := time.Date(2026, 11, 19, 10, 20, 30, 0, time.UTC)

	var tm Time
	for _, src := range []interface{}{
		want,
		"2026-11-19T10:20:30Z",
		[]byte("2026-11-19 10:20:30"),
		[]byte("2026-11-19T10:20:30"),
	} {
		if err := tm.Scan(src); err != nil || !time.Time(tm).Equal(want) {
			t.Errorf("%T(%v): %v %v", src, src, err, time.Time(tm))
		}
	}
	if err := tm.Scan([]byte("2026-11-19")); err != nil || time.Time(tm).Day() != 19 {
		t.Errorf("date only: %v %v", err, time.Time(tm))
	}
	if err := tm.Scan([]byte("not-a-time")); err == nil {
		t.Error("invalid literal should be rejected")
	}
	if err := tm.Scan(nil); err != nil || !tm.IsZero() {
		t.Fatalf("NULL: %v %v", err, tm)
	}

	// The zero time must not reach the frontend as year 0001.
	out, err := json.Marshal(Time(time.Time{}))
	if err != nil || string(out) != "null" {
		t.Fatalf("zero time marshal: %v %s", err, out)
	}
	out, err = json.Marshal(Time(want))
	if err != nil || string(out) != `"2026-11-19T10:20:30Z"` {
		t.Fatalf("marshal: %v %s", err, out)
	}

	assertTimeJSON := func(payload string) {
		t.Helper()
		var got Time
		if err := json.Unmarshal([]byte(payload), &got); err != nil {
			t.Fatalf("unmarshal %s: %v", payload, err)
		}
		if !time.Time(got).Equal(want) {
			t.Fatalf("unmarshal %s = %v", payload, time.Time(got))
		}
	}
	assertTimeJSON(`"2026-11-19T10:20:30Z"`)
	assertTimeJSON(`"2026-11-19 10:20:30"`)

	for _, payload := range []string{"null", `""`} {
		got := Time(want)
		if err := json.Unmarshal([]byte(payload), &got); err != nil || !got.IsZero() {
			t.Fatalf("unmarshal %s: %v %v", payload, err, time.Time(got))
		}
	}
}

// TestJSONB pins the read/write symmetry of JSONB: whatever MarshalJSON can
// hand to the frontend, Value must be able to store, scalars included.
func TestJSONB(t *testing.T) {
	cases := []struct {
		name string
		data interface{}
		want string
	}{
		{"raw object", json.RawMessage(`{"a":1}`), `{"a":1}`},
		{"byte array", []byte(`[1,2]`), `[1,2]`},
		{"number", float64(5), "5"},
		{"boolean", true, "true"},
		{"json string", `"hello"`, `"hello"`},
		{"string holding json", "[1,2]", "[1,2]"},
		{"empty object", map[string]any{}, "{}"},
	}
	for _, c := range cases {
		j := JSONB{Data: c.data}
		api, err := j.MarshalJSON()
		if err != nil || string(api) != c.want {
			t.Errorf("%s: marshal = %s, %v", c.name, api, err)
			continue
		}
		db, err := j.Value()
		if err != nil {
			t.Errorf("%s: value: %v", c.name, err)
			continue
		}
		if b, ok := db.([]byte); !ok || string(b) != c.want {
			t.Errorf("%s: value = %v, want %s", c.name, db, c.want)
		}
	}

	// A value that is not JSON at all must fail loudly instead of becoming NULL.
	if _, err := (JSONB{Data: "hello"}).Value(); err == nil {
		t.Error("invalid JSON should be rejected")
	}

	// NULL, the literal null and empty data all read back as "no data".
	for _, src := range []interface{}{nil, []byte("null"), []byte("")} {
		var j JSONB
		if err := j.Scan(src); err != nil || !j.IsNil() {
			t.Errorf("scan %v: %v %v", src, err, j.Data)
		}
		if v, err := j.Value(); err != nil || v != nil {
			t.Errorf("value of %v: %v %v", src, err, v)
		}
	}

	// An empty object is a value, not a NULL: NOT NULL columns store {} and
	// must read back as {}, otherwise every edit rewrites them as NULL.
	var empty JSONB
	if err := empty.Scan([]byte("{}")); err != nil || empty.IsNil() {
		t.Fatalf("scan empty object: %v %v", err, empty.Data)
	}
	if v, err := empty.Value(); err != nil || string(v.([]byte)) != "{}" {
		t.Fatalf("empty object round trip: %v %v", err, v)
	}
	if out, err := empty.MarshalJSON(); err != nil || string(out) != "{}" {
		t.Fatalf("empty object marshal: %v %s", err, out)
	}

	// A scalar survives a database round trip.
	var j JSONB
	if err := j.Scan([]byte("5")); err != nil || j.IsNil() {
		t.Fatalf("scan scalar: %v %v", err, j.Data)
	}
	if v, err := j.Value(); err != nil || string(v.([]byte)) != "5" {
		t.Fatalf("scalar round trip: %v %v", err, v)
	}
}
