package codex

import (
	"fmt"
	"strings"
	"unicode"
)

// CredentialFileName returns the filename used to persist Codex OAuth credentials.
// When planType is available (e.g. "plus", "team"), it is appended after the email
// as a suffix to disambiguate subscriptions.
func CredentialFileName(email, planType, hashAccountID string, includeProviderPrefix bool) string {
	email = strings.TrimSpace(email)
	plan := normalizePlanTypeForFilename(planType)

	prefix := ""
	if includeProviderPrefix {
		prefix = "codex"
	}

	parts := make([]string, 0, 4)
	if prefix != "" {
		parts = append(parts, prefix)
	}
	if hashAccountID != "" {
		parts = append(parts, hashAccountID)
	}
	if email != "" {
		parts = append(parts, email)
	}
	if plan != "" {
		parts = append(parts, plan)
	}
	if len(parts) == 0 {
		return "codex.json"
	}
	return fmt.Sprintf("%s.json", strings.Join(parts, "-"))
}

func normalizePlanTypeForFilename(planType string) string {
	planType = strings.TrimSpace(planType)
	if planType == "" {
		return ""
	}

	parts := strings.FieldsFunc(planType, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(parts) == 0 {
		return ""
	}

	for i, part := range parts {
		parts[i] = strings.ToLower(strings.TrimSpace(part))
	}
	return strings.Join(parts, "-")
}
