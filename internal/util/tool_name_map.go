package util

import (
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// SanitizedToolNameMap builds a reverse map from sanitized Gemini function names
// back to the original client-facing tool names. Only entries where sanitization
// actually changes the name are included.
func SanitizedToolNameMap(rawJSON []byte) map[string]string {
	if len(rawJSON) == 0 || !gjson.ValidBytes(rawJSON) {
		return nil
	}

	tools := gjson.GetBytes(rawJSON, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return nil
	}

	out := make(map[string]string)
	tools.ForEach(func(_, tool gjson.Result) bool {
		name := strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			return true
		}
		sanitized := SanitizeFunctionName(name)
		if sanitized == name {
			return true
		}
		if _, exists := out[sanitized]; !exists {
			out[sanitized] = name
		} else {
			log.Warnf("sanitized tool name collision: %q and %q both map to %q, keeping first", out[sanitized], name, sanitized)
		}
		return true
	})

	if len(out) == 0 {
		return nil
	}
	return out
}

// RestoreSanitizedToolName looks up a sanitized function name in the provided map
// and returns the original client-facing name. If no mapping exists, it returns
// the sanitized name unchanged.
func RestoreSanitizedToolName(toolNameMap map[string]string, sanitizedName string) string {
	if sanitizedName == "" || toolNameMap == nil {
		return sanitizedName
	}
	if original, ok := toolNameMap[sanitizedName]; ok {
		return original
	}
	return sanitizedName
}
