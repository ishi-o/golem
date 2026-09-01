package webhook

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// DecodeObject decodes a webhook payload and requires its top-level value to
// be a JSON object. Keeping this check in the shared adapter prevents vendor
// sources from accidentally inventing a correlation key for null or an array.
func DecodeObject(body []byte) (map[string]any, error) {
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		return nil, fmt.Errorf("decode JSON object: %w", err)
	}
	if object == nil {
		return nil, fmt.Errorf("webhook payload must be a JSON object")
	}
	return object, nil
}

// ObjectField returns a nested JSON object field.
func ObjectField(object map[string]any, field string) map[string]any {
	value, ok := object[field].(map[string]any)
	if !ok {
		return nil
	}
	return value
}

// ArrayField returns a JSON array field.
func ArrayField(object map[string]any, field string) []any {
	value, ok := object[field].([]any)
	if !ok {
		return nil
	}
	return value
}

// TextField returns a non-blank string field without coercing numbers or
// objects into identity values.
func TextField(object map[string]any, field string) string {
	value, ok := object[field].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return ""
	}
	return value
}

// IntField returns an integral JSON number field. json.Unmarshal represents
// numbers as float64; the range and integral checks avoid silently truncating
// an attacker-controlled fractional or overflowing value.
func IntField(object map[string]any, field string) (int, bool) {
	value, ok := object[field].(float64)
	if !ok || math.Trunc(value) != value || value < math.MinInt || value > math.MaxInt {
		return 0, false
	}
	return int(value), true
}

// SHA256Hex returns the lowercase SHA-256 digest used for delivery fallbacks.
func SHA256Hex(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
