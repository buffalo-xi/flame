package xsql

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// String is a custom string type that turns a database NULL into an empty string.
type String string

// Scan implements sql.Scanner and handles database NULL values.
// This is the core method: it is called automatically when Scan() reads a value from the database.
func (e *String) Scan(value interface{}) error {
	// A NULL database value becomes an empty string.
	if value == nil {
		*e = String("")
		return nil
	}

	// Try to convert the database value to a string.
	// PostgreSQL usually returns text as string, MySQL as []byte.
	switch v := value.(type) {
	case string:
		*e = String(v)
	case []byte:
		*e = String(v)
	default:
		return fmt.Errorf("xsql: cannot convert %T to string", value)
	}
	return nil
}

// Value implements driver.Valuer; it can be adjusted to write a NULL instead.
func (e String) Value() (driver.Value, error) {
	if string(e) == "" {
		return "", nil
	}
	return string(e), nil
}

// Time is a custom time type that turns a database NULL into the zero time.
// Zero time: time.Time{}, i.e. 0001-01-01 00:00:00 +0000 UTC.
// IsZero() returns true for it.
// Note: JSON serialisation renders it as null, the same shape as the NULL written by Value.
//
// Besides time.Time it also accepts the textual forms the MySQL driver returns
// when parseTime is disabled. Such values are parsed in UTC, because the
// driver's location is not visible here.
type Time time.Time

// timeLayouts lists the textual timestamp formats accepted by Scan and UnmarshalJSON.
var timeLayouts = []string{
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	time.RFC3339Nano,
	"2006-01-02",
}

// parseTime parses the textual timestamp forms accepted by this package.
func parseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("xsql: cannot parse %q as time", s)
}

// Scan implements sql.Scanner and handles database NULL values.
// This is the core method: it is called automatically when Scan() reads a value from the database.
func (e *Time) Scan(value interface{}) error {
	// A NULL database value becomes the zero time.
	if value == nil {
		*e = Time(time.Time{})
		return nil
	}

	// Try to convert the database value to a time.
	// PostgreSQL returns time.Time, MySQL returns []byte unless parseTime is enabled.
	switch v := value.(type) {
	case time.Time:
		*e = Time(v)
	case string:
		t, err := parseTime(v)
		if err != nil {
			return err
		}
		*e = Time(t)
	case []byte:
		t, err := parseTime(string(v))
		if err != nil {
			return err
		}
		*e = Time(t)
	default:
		return fmt.Errorf("xsql: cannot convert %T to time.Time", value)
	}
	return nil
}

// Value implements driver.Valuer; it can be adjusted to write a NULL instead.
func (e Time) Value() (driver.Value, error) {
	t := time.Time(e)
	if t.IsZero() {
		return nil, nil
	}
	return t, nil
}

// IsZero reports whether this is the zero time.
func (e Time) IsZero() bool {
	return time.Time(e).IsZero()
}

// String implements fmt.Stringer.
func (e Time) String() string {
	return time.Time(e).String()
}

// MarshalJSON implements json.Marshaler so the time serialises correctly.
// The zero time is rendered as null, matching the NULL written by Value.
func (e Time) MarshalJSON() ([]byte, error) {
	if e.IsZero() {
		return []byte("null"), nil
	}
	return time.Time(e).MarshalJSON()
}

// UnmarshalJSON implements json.Unmarshaler.
// null or an empty string becomes the zero time.
func (e *Time) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*e = Time(time.Time{})
		return nil
	}

	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("xsql: cannot unmarshal %s into time: %w", data, err)
	}
	if s == "" {
		*e = Time(time.Time{})
		return nil
	}

	t, err := parseTime(s)
	if err != nil {
		return err
	}
	*e = Time(t)
	return nil
}

// Bool is a custom boolean type that turns a database NULL into false.
type Bool bool

// Scan implements sql.Scanner and handles database NULL values.
// This is the core method: it is called automatically when Scan() reads a value from the database.
func (e *Bool) Scan(value interface{}) error {
	// A NULL database value becomes false.
	if value == nil {
		*e = Bool(false)
		return nil
	}

	// Try to convert the database value to a boolean.
	// PostgreSQL returns bool, MySQL returns []byte / int64 for tinyint(1).
	switch v := value.(type) {
	case bool:
		*e = Bool(v)
	case int64:
		*e = Bool(v != 0)
	case string:
		b, err := parseBool(v)
		if err != nil {
			return err
		}
		*e = Bool(b)
	case []byte:
		b, err := parseBool(string(v))
		if err != nil {
			return err
		}
		*e = Bool(b)
	default:
		return fmt.Errorf("xsql: cannot convert %T to bool", value)
	}
	return nil
}

// parseBool accepts the textual boolean literals the SQL drivers may return.
func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "t", "true", "on", "yes":
		return true, nil
	case "0", "f", "false", "off", "no":
		return false, nil
	}
	return false, fmt.Errorf("xsql: invalid bool literal %q", s)
}

// Value implements driver.Valuer; it can be adjusted to write a NULL instead.
func (e Bool) Value() (driver.Value, error) {
	return bool(e), nil
}

// Bool returns the boolean value.
func (e Bool) Bool() bool {
	return bool(e)
}

// MarshalJSON implements json.Marshaler so the boolean serialises correctly.
func (e Bool) MarshalJSON() ([]byte, error) {
	if bool(e) {
		return []byte("true"), nil
	}
	return []byte("false"), nil
}

// Int64 is a custom int64 type that turns a database NULL into 0.
type Int64 int64

// Scan implements sql.Scanner and handles database NULL values.
func (e *Int64) Scan(value interface{}) error {
	// A NULL database value becomes 0.
	if value == nil {
		*e = Int64(0)
		return nil
	}

	// Try to convert the database value to an int64.
	// MySQL returns DECIMAL and some integer columns as []byte.
	switch v := value.(type) {
	case int64:
		*e = Int64(v)
	case int:
		*e = Int64(v)
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return fmt.Errorf("xsql: cannot convert %q to int64: %w", v, err)
		}
		*e = Int64(i)
	case []byte:
		i, err := strconv.ParseInt(strings.TrimSpace(string(v)), 10, 64)
		if err != nil {
			return fmt.Errorf("xsql: cannot convert %q to int64: %w", string(v), err)
		}
		*e = Int64(i)
	default:
		return fmt.Errorf("xsql: cannot convert %T to int64", value)
	}
	return nil
}

// Value implements driver.Valuer; it can be adjusted to write a NULL instead.
func (e Int64) Value() (driver.Value, error) {
	return int64(e), nil
}

// Int64 returns the int64 value.
func (e Int64) Int64() int64 {
	return int64(e)
}

// Float64 is a custom float64 type that turns a database NULL into 0.
type Float64 float64

// Scan implements sql.Scanner and handles database NULL values.
func (e *Float64) Scan(value interface{}) error {
	// A NULL database value becomes 0.
	if value == nil {
		*e = Float64(0)
		return nil
	}

	// Try to convert the database value to a float64.
	// MySQL returns DECIMAL as []byte.
	switch v := value.(type) {
	case float64:
		*e = Float64(v)
	case int64:
		*e = Float64(v)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return fmt.Errorf("xsql: cannot convert %q to float64: %w", v, err)
		}
		*e = Float64(f)
	case []byte:
		f, err := strconv.ParseFloat(strings.TrimSpace(string(v)), 64)
		if err != nil {
			return fmt.Errorf("xsql: cannot convert %q to float64: %w", string(v), err)
		}
		*e = Float64(f)
	default:
		return fmt.Errorf("xsql: cannot convert %T to float64", value)
	}
	return nil
}

// Value implements driver.Valuer; it can be adjusted to write a NULL instead.
func (e Float64) Value() (driver.Value, error) {
	return float64(e), nil
}

// Float64 returns the float64 value.
func (e Float64) Float64() float64 {
	return float64(e)
}

// UUID is a custom UUID type that turns a database NULL into an empty string.
type UUID string

// Scan implements sql.Scanner and handles database NULL values.
func (e *UUID) Scan(value interface{}) error {
	// A NULL database value becomes an empty string.
	if value == nil {
		*e = UUID("")
		return nil
	}

	// Try to convert the database value to a string.
	// PostgreSQL usually returns a UUID as []byte.
	// MySQL usually returns a UUID as string.
	switch v := value.(type) {
	case string:
		*e = UUID(v)
		return nil
	case []byte:
		*e = UUID(string(v))
		return nil
	default:
		return fmt.Errorf("xsql: cannot convert %T to uuid", value)
	}
}

// Value implements driver.Valuer; it can be adjusted to write a NULL instead.
func (e UUID) Value() (driver.Value, error) {
	if string(e) == "" {
		return nil, nil
	}
	return string(e), nil
}

// String returns the string value.
func (e UUID) String() string {
	return string(e)
}

// Int is a custom integer type that turns a database NULL into 0.
type Int int

// Scan implements sql.Scanner and handles database NULL values.
func (e *Int) Scan(value interface{}) error {
	// A NULL database value becomes 0.
	if value == nil {
		*e = Int(0)
		return nil
	}

	// Try to convert the database value to an integer.
	switch v := value.(type) {
	case int:
		*e = Int(v)
		return nil
	case int64:
		*e = Int(v)
		return nil
	case []byte:
		// Most SQL drivers return integers as []byte.
		i, err := strconv.Atoi(string(v))
		if err != nil {
			return fmt.Errorf("xsql: cannot convert %T to int: %v", value, err)
		}
		*e = Int(i)
		return nil
	default:
		return fmt.Errorf("xsql: cannot convert %T to int", value)
	}
}

// Value implements driver.Valuer; it can be adjusted to write a NULL instead.
func (e Int) Value() (driver.Value, error) {
	return int64(e), nil
}

// Int returns the integer value.
func (e Int) Int() int {
	return int(e)
}

// JSONB is a custom JSONB type dedicated to PostgreSQL's jsonb columns.
// Properties:
// 1. a database NULL becomes nil; IsNil reports exactly that case
// 2. an empty object is an ordinary value: it is read and written as {}
// 3. JSON serialisation / deserialisation is supported
// 4. conversion to and from Go types is supported
type JSONB struct {
	Data interface{}
}

// Scan implements sql.Scanner and reads jsonb data from the database.
func (j *JSONB) Scan(value interface{}) error {
	if value == nil {
		j.Data = nil
		return nil
	}

	var bytes []byte
	switch v := value.(type) {
	case []byte:
		// PostgreSQL's jsonb type comes back as []byte.
		// Important: the data must be copied, otherwise the reference is shared.
		bytes = make([]byte, len(v))
		copy(bytes, v)
	case string:
		// Some drivers may return a string.
		bytes = []byte(v)
	default:
		return fmt.Errorf("xsql: cannot convert %T to jsonb", value)
	}

	// Empty data means nil.
	if len(bytes) == 0 {
		j.Data = nil
		return nil
	}

	s := string(bytes)

	// The literal null means nil. An empty object is a value, not a NULL: the
	// NOT NULL jsonb columns store {} and must read back as {} so that IsNil
	// really means "the column was NULL".
	if s == "null" {
		j.Data = nil
		return nil
	}

	// Try to parse the JSON to make sure the data is valid.
	var raw json.RawMessage
	if err := json.Unmarshal(bytes, &raw); err == nil {
		// Parsing succeeded: keep it as is.
		j.Data = raw
		return nil
	}
	// Parsing failed: store nil.
	j.Data = nil
	return nil
}

// Value implements driver.Valuer and writes the data into the database.
// It writes exactly the bytes MarshalJSON produces, so whatever the API can
// read back is also what is stored - scalars included.
func (j JSONB) Value() (driver.Value, error) {
	if j.Data == nil {
		return nil, nil
	}

	data, err := j.MarshalJSON()
	if err != nil {
		return nil, err
	}

	// Only null means "no data". An empty object is still a value: NOT NULL
	// jsonb columns (custom tool parameters / configs and friends) are filled
	// with {} precisely so the write does not hit the constraint.
	if string(data) == "null" {
		return nil, nil
	}
	return data, nil
}

// MarshalJSON implements json.Marshaler and produces the frontend payload.
func (j JSONB) MarshalJSON() ([]byte, error) {
	if j.Data == nil {
		return []byte("null"), nil
	}

	// Get the raw data.
	var raw []byte
	switch v := j.Data.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	case json.RawMessage:
		raw = []byte(v)
	default:
		// Anything else must be serialised.
		return json.Marshal(j.Data)
	}

	// Empty data or null becomes null.
	if len(raw) == 0 || string(raw) == "null" {
		return []byte("null"), nil
	}

	// Parse the JSON first, to make sure valid JSON goes out.
	var parsed interface{}
	if err := json.Unmarshal(raw, &parsed); err == nil {
		// Parsed: handle by type.
		switch parsed.(type) {
		case map[string]interface{}, []interface{}:
			// Already a JSON object or array: return it as is.
			return raw, nil
		case string:
			// The result is a string; check whether it is a JSON string.
			strVal := parsed.(string)
			if len(strVal) > 0 && (strVal[0] == '{' || strVal[0] == '[') {
				// Try to parse the JSON inside.
				var innerParsed interface{}
				if err2 := json.Unmarshal([]byte(strVal), &innerParsed); err2 == nil {
					return []byte(strVal), nil
				}
			}
			// Return the string.
			return json.Marshal(strVal)
		default:
			// Anything else is serialised directly.
			return json.Marshal(parsed)
		}
	} else {
		return []byte("null"), err
	}
}

// UnmarshalJSON implements json.Unmarshaler and accepts frontend data.
func (j *JSONB) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		j.Data = nil
		return nil
	}
	// Keep the raw JSON data.
	j.Data = json.RawMessage(data)
	return nil
}

// IsNil reports whether the value is empty.
func (j JSONB) IsNil() bool {
	return j.Data == nil
}

// String returns the JSON string representation.
func (j JSONB) String() string {
	if j.Data == nil {
		return "null"
	}
	switch v := j.Data.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case json.RawMessage:
		return string(v)
	default:
		data, _ := json.Marshal(j.Data)
		return string(data)
	}
}

// Unmarshal parses the JSON into the target value.
func (j JSONB) Unmarshal(v interface{}) error {
	if j.Data == nil {
		return nil
	}

	var data []byte
	switch val := j.Data.(type) {
	case []byte:
		data = val
	case string:
		data = []byte(val)
	case json.RawMessage:
		data = []byte(val)
	default:
		var err error
		data, err = json.Marshal(j.Data)
		if err != nil {
			return err
		}
	}

	return json.Unmarshal(data, v)
}
