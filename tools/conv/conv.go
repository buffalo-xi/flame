// Package conv 提供基础类型转换与反射取值：把 interface{} 安全地转成
// bool / string，或按字段名从 struct / map 中取出属性。
//
// 只做「取值与转换」，不做业务校验；取不到时返回错误而不是 panic。
package conv

import (
	"errors"
	"fmt"
	"reflect"
)

// GetPropertyAsString 从对象中获取属性并转换为字符串
func GetPropertyAsString(obj interface{}, property string) (string, bool, error) {
	if obj == nil {
		return "", false, errors.New("object is nil")
	}

	// 使用反射获取属性
	v := reflect.ValueOf(obj)

	// 处理指针类型
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return "", false, errors.New("object is nil")
		}
		v = v.Elem()
	}

	// 处理结构体
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

	// 处理映射
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

// GetProperty 从对象中获取属性值
func GetProperty(obj interface{}, property string) (interface{}, error) {
	if obj == nil {
		return nil, errors.New("object is nil")
	}

	// 使用反射获取属性
	v := reflect.ValueOf(obj)

	// 处理指针类型
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil, errors.New("object is nil")
		}
		v = v.Elem()
	}

	// 处理结构体
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

	// 处理映射
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

// GetPropertyAsBool 从对象中获取属性并转换为布尔值
// 返回三个参数：布尔值、是否存在、错误
func GetPropertyAsBool(obj interface{}, property string) (bool, bool, error) {
	if obj == nil {
		return false, false, errors.New("object is nil")
	}

	// 使用反射获取属性
	v := reflect.ValueOf(obj)

	// 处理指针类型
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return false, false, errors.New("object is nil")
		}
		v = v.Elem()
	}

	// 处理结构体
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

	// 处理映射
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

// ToBool 将任意类型转换为布尔值
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

// ToString 将任意类型转换为字符串
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
