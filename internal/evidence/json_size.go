package evidence

import (
	"encoding/json"
	"unicode/utf8"
)

// Size counts normalized JSON in the receiving JSON.stringify representation.
// The native sender separately counts its actual serializer's request bytes.
func Size(value any) int {
	nodes := 4096
	remaining := 32768
	var count func(any, int) bool
	count = func(value any, depth int) bool {
		nodes--
		if nodes < 0 || depth > 16 || remaining < 0 {
			return false
		}
		switch item := value.(type) {
		case nil:
			remaining -= 4
		case bool:
			if item {
				remaining -= 4
			} else {
				remaining -= 5
			}
		case string:
			if len(item) > remaining || !utf8.ValidString(item) {
				return false
			}
			remaining -= 2
			for _, r := range item {
				switch r {
				case '"', '\\', '\b', '\f', '\n', '\r', '\t':
					remaining -= 2
				default:
					if r < 32 {
						remaining -= 6
					} else {
						remaining -= utf8.RuneLen(r)
					}
				}
				if remaining < 0 {
					return false
				}
			}
		case float64:
            if item==0{remaining--;break}
			encoded, err := json.Marshal(item)
			if err != nil {
				return false
			}
			remaining -= len(encoded)
		case map[string]any:
			if len(item) > 256 {
				return false
			}
			remaining -= 2
			if len(item) > 0 {
				remaining -= len(item) - 1
			}
			for key, child := range item {
				if !count(key, depth+1) {
					return false
				}
				remaining--
				if !count(child, depth+1) {
					return false
				}
			}
		case []any:
			if len(item) > 256 {
				return false
			}
			remaining -= 2
			if len(item) > 0 {
				remaining -= len(item) - 1
			}
			for _, child := range item {
				if !count(child, depth+1) {
					return false
				}
			}
		default:
			return false
		}
		return remaining >= 0
	}
	if !count(value, 0) {
		return 32769
	}
	return 32768 - remaining
}
