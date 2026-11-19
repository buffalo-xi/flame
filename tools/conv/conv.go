// Package conv provides basic type conversion and reflection-based property
// access: safely turn an interface{} into a bool / string, or read a property
// from a struct / map by field name.
//
// It only reads and converts — no business validation — and reports a missing
// property as an error instead of panicking.
package conv

import (
	"errors"
	"fmt"
	"reflect"
)

// GetPropertyAsString reads a property from an object and converts it to a string.
func GetPropertyAsString(obj interface{}, property string) (string, bool, error) {
	if obj == nil {
		return "", false, errors.New("object is nil")
	}

	// Read the property through reflection.
	v := reflect.ValueOf(obj)

	// Handle pointer types.
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return "", false, errors.New("object is nil")
		}
		v = v.Elem()
	}

	// Handle structs.
	if v.Kind() == reflect.Struct {
		field := v.FieldByName(property)
		if !field.IsValid() {
			return "", false, errors.New("property not found")
		}
		if !field.CanInterface() {
			return "", true, errors.New("property cannot be accessed")
		}
		return fmt.Sprint(field.Interface()), true, nil
	}

	// Handle maps.
	if v.Kind() == reflect.Map {
		key := reflect.ValueOf(property)
		value := v.MapIndex(key)
		if !value.IsValid() {
			return "", false, errors.New("property not found")
		}
		return fmt.Sprint(value.Interface()), true, nil
	}

	return "", false, errors.New("unsupported object type")
}

// GetProperty reads a property value from an object.
func GetProperty(obj interface{}, property string) (interface{}, error) {
	if obj == nil {
		return nil, errors.New("object is nil")
	}

	// Read the property through reflection.
	v := reflect.ValueOf(obj)

	// Handle pointer types.
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil, errors.New("object is nil")
		}
		v = v.Elem()
	}

	// Handle structs.
	if v.Kind() == reflect.Struct {
		field := v.FieldByName(property)
		if !field.IsValid() {
			return nil, errors.New("property not found")
		}
		if !field.CanInterface() {
			return nil, errors.New("property cannot be accessed")
		}
		return field.Interface(), nil
	}

	// Handle maps.
	if v.Kind() == reflect.Map {
		key := reflect.ValueOf(property)
		value := v.MapIndex(key)
		if !value.IsValid() {
			return nil, errors.New("property not found")
		}
		return value.Interface(), nil
	}

	return nil, errors.New("unsupported object type")
}

// GetPropertyAsBool reads a property from an object and converts it to a bool.
// It returns three values: the bool, whether the property existed, and an error.
func GetPropertyAsBool(obj interface{}, property string) (bool, bool, error) {
	if obj == nil {
		return false, false, errors.New("object is nil")
	}

	// Read the property through reflection.
	v := reflect.ValueOf(obj)

	// Handle pointer types.
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return false, false, errors.New("object is nil")
		}
		v = v.Elem()
	}

	// Handle structs.
	if v.Kind() == reflect.Struct {
		field := v.FieldByName(property)
		if !field.IsValid() {
			return false, false, nil
		}
		if !field.CanInterface() {
			return false, true, errors.New("property cannot be accessed")
		}
		value := field.Interface()
		return ToBool(value), true, nil
	}

	// Handle maps.
	if v.Kind() == reflect.Map {
		key := reflect.ValueOf(property)
		value := v.MapIndex(key)
		if !value.IsValid() {
			return false, false, nil
		}
		return ToBool(value.Interface()), true, nil
	}

	return false, false, errors.New("unsupported object type")
}

// ToBool converts any value to a bool.
func ToBool(value interface{}) bool {
	if value == nil {
		return false
	}

	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Bool:
		return v.Bool()
	case reflect.String:
		s := v.String()
		return s == "true" || s == "1" || s == "on" || s == "yes"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() != 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() != 0
	case reflect.Float32, reflect.Float64:
		return v.Float() != 0
	case reflect.Interface, reflect.Ptr:
		if v.IsNil() {
			return false
		}
		return ToBool(v.Elem().Interface())
	default:
		return false
	}
}

// ToString converts any value to a string.
func ToString(value interface{}) string {
	if value == nil {
		return ""
	}

	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Bool:
		if v.Bool() {
			return "true"
		}
		return "false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return fmt.Sprintf("%d", v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return fmt.Sprintf("%d", v.Uint())
	case reflect.Float32, reflect.Float64:
		return fmt.Sprintf("%g", v.Float())
	case reflect.Interface, reflect.Ptr:
		if v.IsNil() {
			return ""
		}
		return ToString(v.Elem().Interface())
	default:
		return ""
	}
}
