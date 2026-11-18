package xjson

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// JSONValidatorRule defines validation rules for JSON data
type JSONValidatorRule struct {
	Key                 string    // Field name in JSON
	Required            bool      // Whether the field is required
	Min                 float64   // Minimum value for numbers
	Max                 float64   // Maximum value for numbers
	IsInteger           bool      // Whether number must be an integer
	HasNumberFixedValue bool      // Whether number must equal a fixed value
	NumberFixedValue    float64   // Number must equal this value
	NumberOptions       []float64 // Number must be in this list
	IsDate              bool      // Whether string must be a date
	DateFormat          string    // Date format for validation
	SkipDateValidation  bool      // Whether to skip date validation (default: false)
	AllowEmpty          bool      // Whether empty string is allowed
	FixedValue          string    // Expected fixed value for strings
	Options             []string  // List of allowed values for strings
	Message             string    // Custom error message
}

// GetJSONData gets JSON data from Gin context and returns as a map
func GetJSONData(c *gin.Context, result interface{}) error {
	if err := c.ShouldBindJSON(result); err != nil {
		return fmt.Errorf("invalid JSON format: %w", err)
	}
	return nil
}

// ValidateJSONData validates JSON data against specified rules
func ValidateJSONData(data map[string]interface{}, rules []JSONValidatorRule) error {
	for _, rule := range rules {
		// Check if field exists
		value, exists := data[rule.Key]
		if !exists {
			if rule.Required {
				msg := rule.Message
				if msg == "" {
					msg = fmt.Sprintf("%s is required", rule.Key)
				}
				return errors.New(msg)
			}
			continue
		}

		// Check based on data type
		switch v := value.(type) {
		case float64: // JSON numbers are float64 by default
			// Check if must be integer
			if rule.IsInteger {
				if v != float64(int64(v)) {
					msg := rule.Message
					if msg == "" {
						msg = fmt.Sprintf("%s must be an integer", rule.Key)
					}
					return errors.New(msg)
				}
			}

			// Check range
			if rule.Min != 0 || rule.Max != 0 {
				if v < rule.Min {
					msg := rule.Message
					if msg == "" {
						msg = fmt.Sprintf("%s must be greater than or equal to %f", rule.Key, rule.Min)
					}
					return errors.New(msg)
				}
				if rule.Max != 0 && v > rule.Max {
					msg := rule.Message
					if msg == "" {
						msg = fmt.Sprintf("%s must be less than or equal to %f", rule.Key, rule.Max)
					}
					return errors.New(msg)
				}
			}

			// Check fixed value
			if rule.HasNumberFixedValue {
				if v != rule.NumberFixedValue {
					msg := rule.Message
					if msg == "" {
						msg = fmt.Sprintf("%s must be equal to %f", rule.Key, rule.NumberFixedValue)
					}
					return errors.New(msg)
				}
			}

			// Check number options
			if len(rule.NumberOptions) > 0 {
				found := false
				for _, opt := range rule.NumberOptions {
					if v == opt {
						found = true
						break
					}
				}
				if !found {
					msg := rule.Message
					if msg == "" {
						msg = fmt.Sprintf("%s must be one of: %v", rule.Key, rule.NumberOptions)
					}
					return errors.New(msg)
				}
			}

		case string:
			// Check empty string
			if !rule.AllowEmpty && v == "" {
				msg := rule.Message
				if msg == "" {
					msg = fmt.Sprintf("%s cannot be empty", rule.Key)
				}
				return errors.New(msg)
			}

			// Check if must be a date
			if rule.IsDate {
				// Use default date format if none specified
				dateFormat := rule.DateFormat
				if dateFormat == "" {
					dateFormat = "2006-01-02"
				}

				// Validate date format and optionally validate date validity
				// Default to validate date, only skip if SkipDateValidation is explicitly set to true
				if rule.SkipDateValidation {
					// Skip date validation if explicitly enabled
				} else {
					// Validate date format
					_, err := time.Parse(dateFormat, v)
					if err != nil {
						msg := rule.Message
						if msg == "" {
							msg = fmt.Sprintf("%s must be a valid date in format %s", rule.Key, dateFormat)
						}
						return errors.New(msg)
					}
				}
			}

			// Check fixed value
			if rule.FixedValue != "" && v != rule.FixedValue {
				msg := rule.Message
				if msg == "" {
					msg = fmt.Sprintf("%s must be %s", rule.Key, rule.FixedValue)
				}
				return errors.New(msg)
			}

			// Check options
			if len(rule.Options) > 0 {
				found := false
				for _, opt := range rule.Options {
					if v == opt {
						found = true
						break
					}
				}
				if !found {
					msg := rule.Message
					if msg == "" {
						msg = fmt.Sprintf("%s must be one of: %v", rule.Key, rule.Options)
					}
					return errors.New(msg)
				}
			}
		}
	}
	return nil
}

// ValidateAndGetJSON validates JSON data from Gin context and returns validated map
func ValidateAndGetJSON(c *gin.Context, rules []JSONValidatorRule) (map[string]interface{}, error) {
	var data map[string]interface{}
	if err := GetJSONData(c, &data); err != nil {
		return nil, err
	}

	if err := ValidateJSONData(data, rules); err != nil {
		return nil, err
	}

	return data, nil
}

// GetInt64FromJSON gets an int64 value from JSON data
func GetInt64FromJSON(data map[string]interface{}, key string, defaultValue int64) int64 {
	value, exists := data[key]
	if !exists {
		return defaultValue
	}

	switch v := value.(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case string:
		if i, err := strconv.ParseInt(v, 10, 64); err == nil {
			return i
		}
	}
	return defaultValue
}

// GetStringFromJSON gets a string value from JSON data
func GetStringFromJSON(data map[string]interface{}, key string, defaultValue string) string {
	value, exists := data[key]
	if !exists {
		return defaultValue
	}

	if str, ok := value.(string); ok {
		return str
	}
	return defaultValue
}

// GetFloat64FromJSON gets a float64 value from JSON data
func GetFloat64FromJSON(data map[string]interface{}, key string, defaultValue float64) float64 {
	value, exists := data[key]
	if !exists {
		return defaultValue
	}

	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return defaultValue
}

// GetBoolFromJSON gets a boolean value from JSON data
func GetBoolFromJSON(data map[string]interface{}, key string, defaultValue bool) bool {
	value, exists := data[key]
	if !exists {
		return defaultValue
	}

	if b, ok := value.(bool); ok {
		return b
	}
	return defaultValue
}

// ExtractJSONField extracts a field from JSON data using reflection
func ExtractJSONField(data interface{}, fieldName string) (interface{}, error) {
	dv := reflect.ValueOf(data)
	if dv.Kind() == reflect.Ptr {
		dv = dv.Elem()
	}
	if dv.Kind() != reflect.Struct {
		return nil, fmt.Errorf("data is not a struct")
	}

	fv := dv.FieldByName(fieldName)
	if !fv.IsValid() {
		return nil, fmt.Errorf("field %s not found", fieldName)
	}

	return fv.Interface(), nil
}

var (
	// 匹配所有 \uXXXX 格式
	unicodeRegex = regexp.MustCompile(`\\u[0-9a-fA-F]{4}`)
	// 匹配零宽字符
	//zeroWidthRegex = regexp.MustCompile("[\u200B\u200C\u200D\uFEFF]")

	// 匹配所有不可见字符（包含零宽、控制符、所有看不见的）
	invisibleRegex = regexp.MustCompile("[\x00-\x08\x0B\x0C\x0E-\x1F\x7F\u200B\u200C\u200D\u200E\u200F\u2028\u2029\uFEFF]")
	// 匹配非法转义
	invalidEscapeRegex = regexp.MustCompile(`\\[^"\\/bfnrt]`)
)

func SanitizeJSON(input string) string {
	// 1. 清除所有不可见字符（核心！你要的功能）
	input = invisibleRegex.ReplaceAllString(input, "")

	// 2. 把 \u003e 这类字符还原成正常字符
	input = unicodeRegex.ReplaceAllStringFunc(input, func(m string) string {
		code, err := strconv.ParseInt(m[2:], 16, 32)
		if err != nil {
			return ""
		}
		return string(rune(code))
	})

	// 3. 清理非法转义 \s \m \g 等
	input = invalidEscapeRegex.ReplaceAllString(input, "")

	// 4. 转义双引号 " → \" 防止 JSON 断裂
	input = strings.ReplaceAll(input, `"`, `\"`)

	//// 1. 清理零宽字符
	//input = zeroWidthRegex.ReplaceAllString(input, "")
	//// 2. 移除非法 Unicode 转义
	//input = unicodeRegex.ReplaceAllStringFunc(input, func(m string) string {
	//	// 尝试解析为正常字符，解析失败则删除
	//	r, ok := decodeUnicode(m)
	//	if ok {
	//		return string(r)
	//	}
	//	return ""
	//})
	//// 3. 清理非法转义（如 \s \m \g）
	//input = invalidEscapeRegex.ReplaceAllString(input, "")

	return input
}

// 解码 \uXXXX
func decodeUnicode(s string) (rune, bool) {
	if len(s) != 6 || !strings.HasPrefix(s, "\\u") {
		return 0, false
	}
	var r rune
	_, err := fmt.Sscanf(s[2:], "%04x", &r)
	return r, err == nil
}

func MarshalJSON(data interface{}) string {
	jsonString, _ := json.Marshal(data)
	if jsonString == nil {
		return ""
	}
	return string(jsonString)
}

func ConvertToJSONNumber(value interface{}) (interface{}, error) {
	if value == nil {
		return nil, nil
	}

	data, err := json.Marshal(value)
	if err != nil {
		return value, err
	}

	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()

	var result interface{}
	if err := dec.Decode(&result); err != nil {
		return value, err
	}

	return result, nil
}
