package xsql

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// String 自定义字符串类型：数据库NULL值自动转为空串
type String string

// Scan 实现sql.Scanner接口，处理数据库NULL值
// 这是核心方法，会在Scan()读取数据库值时自动调用
func (e *String) Scan(value interface{}) error {
	// 如果数据库值为NULL，直接赋值为空串
	if value == nil {
		*e = String("")
		return nil
	}

	// 尝试将数据库值转换为字符串
	str, ok := value.(string)
	if !ok {
		return fmt.Errorf("无法将%T类型的值转换为字符串", value)
	}

	// 非NULL值直接赋值
	*e = String(str)
	return nil
}

// Value 实现driver.Valuer接口，可以调整为写入空值null
func (e String) Value() (driver.Value, error) {
	if string(e) == "" {
		return "", nil
	}
	return string(e), nil
}

// Time 自定义时间类型：数据库NULL值自动转为零值时间
// 零值时间：time.Time{}
// 对应 0001-01-01 00:00:00 +0000 UTC
// 特点：IsZero() 返回 true
// 注意：JSON序列化时会显示为 "0001-01-01T00:00:00Z"
type Time time.Time

// Scan 实现sql.Scanner接口，处理数据库NULL值
// 这是核心方法，会在Scan()读取数据库值时自动调用
func (e *Time) Scan(value interface{}) error {
	// 如果数据库值为NULL，直接赋值为零值时间
	if value == nil {
		*e = Time(time.Time{})
		return nil
	}

	// 尝试将数据库值转换为时间
	t, ok := value.(time.Time)
	if !ok {
		return fmt.Errorf("无法将%T类型的值转换为时间", value)
	}

	// 非NULL值直接赋值
	*e = Time(t)
	return nil
}

// Value 实现driver.Valuer接口，可以调整为写入空值null
func (e Time) Value() (driver.Value, error) {
	t := time.Time(e)
	if t.IsZero() {
		return nil, nil
	}
	return t, nil
}

// IsZero 检查是否为零值时间
func (e Time) IsZero() bool {
	return time.Time(e).IsZero()
}

// String 实现fmt.Stringer接口
func (e Time) String() string {
	return time.Time(e).String()
}

// MarshalJSON 实现json.Marshaler接口，确保时间能够正确序列化
func (e Time) MarshalJSON() ([]byte, error) {
	return time.Time(e).MarshalJSON()
}

// Bool 自定义布尔类型：数据库NULL值自动转为false
type Bool bool

// Scan 实现sql.Scanner接口，处理数据库NULL值
// 这是核心方法，会在Scan()读取数据库值时自动调用
func (e *Bool) Scan(value interface{}) error {
	// 如果数据库值为NULL，直接赋值为false
	if value == nil {
		*e = Bool(false)
		return nil
	}

	// 尝试将数据库值转换为布尔值
	b, ok := value.(bool)
	if !ok {
		return fmt.Errorf("无法将%T类型的值转换为布尔值", value)
	}

	// 非NULL值直接赋值
	*e = Bool(b)
	return nil
}

// Value 实现driver.Valuer接口，可以调整为写入空值null
func (e Bool) Value() (driver.Value, error) {
	return bool(e), nil
}

// Bool 返回布尔值
func (e Bool) Bool() bool {
	return bool(e)
}

// MarshalJSON 实现json.Marshaler接口，确保布尔值能够正确序列化
func (e Bool) MarshalJSON() ([]byte, error) {
	if bool(e) {
		return []byte("true"), nil
	}
	return []byte("false"), nil
}

// Int64 自定义int64类型：数据库NULL值自动转为0
type Int64 int64

// Scan 实现sql.Scanner接口，处理数据库NULL值
func (e *Int64) Scan(value interface{}) error {
	// 如果数据库值为NULL，直接赋值为0
	if value == nil {
		*e = Int64(0)
		return nil
	}

	// 尝试将数据库值转换为int64
	i, ok := value.(int64)
	if !ok {
		return fmt.Errorf("无法将%T类型的值转换为int64", value)
	}

	// 非NULL值直接赋值
	*e = Int64(i)
	return nil
}

// Value 实现driver.Valuer接口，可以调整为写入空值null
func (e Int64) Value() (driver.Value, error) {
	return int64(e), nil
}

// Int64 返回int64值
func (e Int64) Int64() int64 {
	return int64(e)
}

// Float64 自定义float64类型：数据库NULL值自动转为0
type Float64 float64

// Scan 实现sql.Scanner接口，处理数据库NULL值
func (e *Float64) Scan(value interface{}) error {
	// 如果数据库值为NULL，直接赋值为0
	if value == nil {
		*e = Float64(0)
		return nil
	}

	// 尝试将数据库值转换为float64
	f, ok := value.(float64)
	if !ok {
		return fmt.Errorf("无法将%T类型的值转换为float64", value)
	}

	// 非NULL值直接赋值
	*e = Float64(f)
	return nil
}

// Value 实现driver.Valuer接口，可以调整为写入空值null
func (e Float64) Value() (driver.Value, error) {
	return float64(e), nil
}

// Float64 返回float64值
func (e Float64) Float64() float64 {
	return float64(e)
}

// UUID 自定义UUID类型：数据库NULL值自动转为空串
type UUID string

// Scan 实现sql.Scanner接口，处理数据库NULL值
func (e *UUID) Scan(value interface{}) error {
	// 如果数据库值为NULL，直接赋值为空串
	if value == nil {
		*e = UUID("")
		return nil
	}

	// 尝试将数据库值转换为字符串
	// PostgreSQL通常返回[]byte类型的UUID
	// MySQL通常返回string类型的UUID
	switch v := value.(type) {
	case string:
		*e = UUID(v)
		return nil
	case []byte:
		*e = UUID(string(v))
		return nil
	default:
		return fmt.Errorf("无法将%T类型的值转换为UUID", value)
	}
}

// Value 实现driver.Valuer接口，可以调整为写入空值null
func (e UUID) Value() (driver.Value, error) {
	if string(e) == "" {
		return nil, nil
	}
	return string(e), nil
}

// String 返回字符串值
func (e UUID) String() string {
	return string(e)
}

// Int 自定义整数类型：数据库NULL值自动转为0
type Int int

// Scan 实现sql.Scanner接口，处理数据库NULL值
func (e *Int) Scan(value interface{}) error {
	// 如果数据库值为NULL，直接赋值为0
	if value == nil {
		*e = Int(0)
		return nil
	}

	// 尝试将数据库值转换为整数
	switch v := value.(type) {
	case int:
		*e = Int(v)
		return nil
	case int64:
		*e = Int(v)
		return nil
	case []byte:
		// 大多数 SQL 驱动将整数作为 []byte 返回
		i, err := strconv.Atoi(string(v))
		if err != nil {
			return fmt.Errorf("无法将%T类型的值转换为整数: %v", value, err)
		}
		*e = Int(i)
		return nil
	default:
		return fmt.Errorf("无法将%T类型的值转换为整数", value)
	}
}

// Value 实现driver.Valuer接口，可以调整为写入空值null
func (e Int) Value() (driver.Value, error) {
	return int64(e), nil
}

// Int 返回整数值
func (e Int) Int() int {
	return int(e)
}

// JSONB 自定义JSONB类型：数据库NULL值自动转为空JSON
//type JSON []byte
//
//// Scan 实现sql.Scanner接口，处理数据库NULL值
//func (e *JSON) Scan(value interface{}) error {
//	// 如果数据库值为NULL，直接赋值为空字符串
//	if value == nil {
//		*e = JSON("null")
//		return nil
//	}
//
//	// 尝试将数据库值转换为字符串
//	switch v := value.(type) {
//	case []byte:
//		*e = JSON(v)
//		return nil
//	case string:
//		*e = JSON(v)
//		return nil
//	default:
//		return fmt.Errorf("无法将%T类型的值转换为JSON", value)
//	}
//}
//
//// Value 实现driver.Valuer接口，可以调整为写入空值null
//func (e JSON) Value() (driver.Value, error) {
//	if string(e) == "" || string(e) == "null" {
//		return nil, nil
//	}
//	return string(e), nil
//}
//
//// MarshalJSON 实现json.Marshaler接口，确保JSONB能够正确序列化
//func (e *JSON) MarshalJSON() ([]byte, error) {
//	// 空值 / 空字符串 → 返回 null（合法JSON）
//	if e == nil || len(*e) == 0 || string(*e) == "" {
//		return []byte("null"), nil
//	}
//
//	// 内容是 "null" 字符串 → 转成 null
//	s := string(*e)
//	if s == "null" {
//		return []byte("null"), nil
//	}
//
//	return *e, nil
//}
//
//// UnmarshalJSON 实现json.Unmarshaler接口，确保JSON能够正确反序列化
//func (e *JSON) UnmarshalJSON(data []byte) error {
//	if len(data) == 0 || string(data) == "" {
//		*e = JSON("")
//		return nil
//	}
//	// 否则，直接使用原始数据
//	*e = JSON(data)
//	return nil
//}
//
//// String 返回字符串值
//func (e JSON) String() string {
//	return string(e)
//}

// JSON 自定义jsonb类型
type JSON []byte

// MarshalJSON 【输出给前端】
func (j JSON) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("null"), nil
	}
	return j, nil
}

// UnmarshalJSON 【接收前端数据】
func (j *JSON) UnmarshalJSON(data []byte) error {
	if j == nil {
		return errors.New("xsql.JSON: unmarshal on nil pointer")
	}
	*j = append((*j)[0:0], data...)
	return nil
}

// Value 【存入数据库】
func (j JSON) Value() (driver.Value, error) {
	if len(j) == 0 {
		return nil, nil
	}
	return []byte(j), nil
}

// Scan 【从数据库 jsonb 读取】
func (j *JSON) Scan(src any) error {
	if src == nil {
		*j = JSON("null")
		return nil
	}

	var bytes []byte
	switch v := src.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return fmt.Errorf("unsupported type: %T", src)
	}

	*j = append((*j)[0:0], bytes...)
	return nil
}

// JSONB 自定义JSONB类型：专门支持PostgreSQL的jsonb列类型
// 特性：
// 1. 数据库NULL值自动转为nil
// 2. 支持JSON序列化/反序列化
// 3. 支持与Go类型的相互转换
type JSONB struct {
	Data interface{}
}

// Scan 实现sql.Scanner接口，从数据库读取jsonb数据
func (j *JSONB) Scan(value interface{}) error {
	if value == nil {
		j.Data = nil
		return nil
	}

	var bytes []byte
	switch v := value.(type) {
	case []byte:
		// PostgreSQL的jsonb类型返回[]byte
		// 重要：必须复制数据，避免引用问题
		bytes = make([]byte, len(v))
		copy(bytes, v)
	case string:
		// 某些驱动可能返回string
		bytes = []byte(v)
	default:
		return fmt.Errorf("无法将%T类型的值转换为JSONB", value)
	}

	// 如果是空数据，设置为nil
	if len(bytes) == 0 {
		j.Data = nil
		return nil
	}

	s := string(bytes)

	// 如果是null字符串或空对象，设置为nil
	if s == "null" || s == "{}" {
		j.Data = nil
		return nil
	}

	// 尝试解析JSON，确保数据是有效的
	var raw json.RawMessage
	if err := json.Unmarshal(bytes, &raw); err == nil {
		// 解析成功，直接保存
		j.Data = raw
		return nil
	}
	// 如果还是失败，保存原始字节
	j.Data = nil
	return nil
}

// Value 实现driver.Valuer接口，将数据写入数据库
func (j JSONB) Value() (driver.Value, error) {
	if j.Data == nil {
		return nil, nil
	}

	// 获取原始数据字节
	var rawData []byte
	switch v := j.Data.(type) {
	case []byte:
		rawData = v
	case json.RawMessage:
		rawData = []byte(v)
	case string:
		rawData = []byte(v)
	default:
		// 其他类型直接序列化
		b, err := json.Marshal(j.Data)
		if err != nil {
			return nil, err
		}
		rawData = b
	}

	// 空数据处理
	if len(rawData) == 0 || string(rawData) == "null" {
		return nil, nil
	}

	// 先尝试解析JSON，检查结果类型
	var parsed interface{}
	if err := json.Unmarshal(rawData, &parsed); err == nil {
		switch parsed.(type) {
		case map[string]interface{}, []interface{}:
			// 已经是JSON对象或数组，直接返回
			return rawData, nil
		case string:
			// 解析结果是字符串，检查是否是JSON字符串
			strVal := parsed.(string)
			if len(strVal) > 0 && (strVal[0] == '{' || strVal[0] == '[') {
				// 尝试解析内部的JSON
				var innerParsed interface{}
				if err2 := json.Unmarshal([]byte(strVal), &innerParsed); err2 == nil {
					// 成功，返回内部的JSON字节
					return []byte(strVal), nil
				}
			}
			return nil, nil
		default:
			// 其他类型（数字、布尔等），直接返回
			return nil, nil
		}
	}
	return nil, nil
}

// MarshalJSON 实现json.Marshaler接口，输出给前端
func (j JSONB) MarshalJSON() ([]byte, error) {
	if j.Data == nil {
		return []byte("null"), nil
	}

	// 获取原始数据
	var raw []byte
	switch v := j.Data.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	case json.RawMessage:
		raw = []byte(v)
	default:
		// 其他类型需要序列化
		return json.Marshal(j.Data)
	}

	// 如果是空数据或null，返回null
	if len(raw) == 0 || string(raw) == "null" {
		return []byte("null"), nil
	}

	// 先尝试解析JSON，确保返回有效的JSON
	var parsed interface{}
	if err := json.Unmarshal(raw, &parsed); err == nil {
		// 解析成功，根据类型处理
		switch parsed.(type) {
		case map[string]interface{}, []interface{}:
			// 已经是JSON对象或数组，直接返回
			return raw, nil
		case string:
			// 解析结果是字符串，检查是否是JSON字符串
			strVal := parsed.(string)
			if len(strVal) > 0 && (strVal[0] == '{' || strVal[0] == '[') {
				// 尝试解析内部的JSON
				var innerParsed interface{}
				if err2 := json.Unmarshal([]byte(strVal), &innerParsed); err2 == nil {
					return []byte(strVal), nil
				}
			}
			// 返回字符串
			return json.Marshal(strVal)
		default:
			// 其他类型，直接序列化
			return json.Marshal(parsed)
		}
	} else {
		return []byte("null"), err
	}
}

// UnmarshalJSON 实现json.Unmarshaler接口，接收前端数据
func (j *JSONB) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		j.Data = nil
		return nil
	}
	// 保存原始JSON数据
	j.Data = json.RawMessage(data)
	return nil
}

// IsNil 检查是否为空
func (j JSONB) IsNil() bool {
	return j.Data == nil
}

// String 返回JSON字符串表示
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

// Unmarshal 解析JSON到目标类型
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
