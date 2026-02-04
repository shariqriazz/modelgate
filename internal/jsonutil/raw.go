package jsonutil

import "encoding/json"

// RawValue marshals a value into raw JSON bytes for sjson.SetRawBytes calls.
// It returns false when the value cannot be represented or is intentionally empty.
func RawValue(value any) ([]byte, bool) {
	if value == nil {
		return nil, false
	}
	switch typed := value.(type) {
	case string:
		return []byte(typed), true
	case []byte:
		return typed, true
	default:
		raw, errMarshal := json.Marshal(typed)
		if errMarshal != nil {
			return nil, false
		}
		return raw, true
	}
}
