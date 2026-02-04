package executor

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/shariqriazz/modelgate/internal/config"
	"github.com/shariqriazz/modelgate/internal/jsonutil"
	"github.com/shariqriazz/modelgate/internal/thinking"
	"github.com/shariqriazz/modelgate/internal/util"
	cliproxyexecutor "github.com/shariqriazz/modelgate/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ApplyThinkingMetadata applies thinking config from model suffix metadata (e.g., (high), (8192))
// for standard Gemini format payloads. It normalizes the budget when the model supports thinking.
func ApplyThinkingMetadata(payload []byte, metadata map[string]any, model string) []byte {
	// Use the alias from metadata if available, as it's registered in the global registry
	// with thinking metadata; the upstream model name may not be registered.
	lookupModel := util.ResolveOriginalModel(model, metadata)

	// Determine which model to use for thinking support check.
	// If the alias (lookupModel) is not in the registry, fall back to the upstream model.
	thinkingModel := lookupModel
	if !util.ModelSupportsThinking(lookupModel) && util.ModelSupportsThinking(model) {
		thinkingModel = model
	}

	budgetOverride, includeOverride, ok := util.ResolveThinkingConfigFromMetadata(thinkingModel, metadata)
	if !ok || (budgetOverride == nil && includeOverride == nil) {
		return payload
	}
	if !util.ModelSupportsThinking(thinkingModel) {
		return payload
	}
	if budgetOverride != nil {
		norm := util.NormalizeThinkingBudget(thinkingModel, *budgetOverride)
		budgetOverride = &norm
	}
	return util.ApplyGeminiThinkingConfig(payload, budgetOverride, includeOverride)
}

// ApplyThinkingMetadataCLI applies thinking config from model suffix metadata (e.g., (high), (8192))
// for Gemini CLI format payloads (nested under "request"). It normalizes the budget when the model supports thinking.
func ApplyThinkingMetadataCLI(payload []byte, metadata map[string]any, model string) []byte {
	// Use the alias from metadata if available, as it's registered in the global registry
	// with thinking metadata; the upstream model name may not be registered.
	lookupModel := util.ResolveOriginalModel(model, metadata)

	// Determine which model to use for thinking support check.
	// If the alias (lookupModel) is not in the registry, fall back to the upstream model.
	thinkingModel := lookupModel
	if !util.ModelSupportsThinking(lookupModel) && util.ModelSupportsThinking(model) {
		thinkingModel = model
	}

	budgetOverride, includeOverride, ok := util.ResolveThinkingConfigFromMetadata(thinkingModel, metadata)
	if !ok || (budgetOverride == nil && includeOverride == nil) {
		return payload
	}
	if !util.ModelSupportsThinking(thinkingModel) {
		return payload
	}
	if budgetOverride != nil {
		norm := util.NormalizeThinkingBudget(thinkingModel, *budgetOverride)
		budgetOverride = &norm
	}
	return util.ApplyGeminiCLIThinkingConfig(payload, budgetOverride, includeOverride)
}

// ApplyReasoningEffortMetadata applies reasoning effort overrides from metadata to the given JSON path.
// Metadata values take precedence over any existing field when the model supports thinking, intentionally
// overwriting caller-provided values to honor suffix/default metadata priority.
func ApplyReasoningEffortMetadata(payload []byte, metadata map[string]any, model, field string, allowCompat bool) []byte {
	if len(metadata) == 0 {
		return payload
	}
	if field == "" {
		return payload
	}
	baseModel := util.ResolveOriginalModel(model, metadata)
	if baseModel == "" {
		baseModel = model
	}
	if !util.ModelSupportsThinking(baseModel) && !allowCompat {
		return payload
	}
	if effort, ok := util.ReasoningEffortFromMetadata(metadata); ok && effort != "" {
		if util.ModelUsesThinkingLevels(baseModel) || allowCompat {
			if updated, err := sjson.SetBytes(payload, field, effort); err == nil {
				return updated
			}
		}
	}
	// Fallback: numeric thinking_budget suffix for level-based (OpenAI-style) models.
	if util.ModelUsesThinkingLevels(baseModel) || allowCompat {
		if budget, _, _, matched := util.ThinkingFromMetadata(metadata); matched && budget != nil {
			if effort, ok := util.ThinkingBudgetToEffort(baseModel, *budget); ok && effort != "" {
				if updated, err := sjson.SetBytes(payload, field, effort); err == nil {
					return updated
				}
			}
		}
	}
	return payload
}

// applyPayloadConfigWithRoot behaves like applyPayloadConfig but treats all parameter
// paths as relative to the provided root path (for example, "request" for Gemini CLI)
// and restricts matches to the given protocol when supplied. Defaults are checked
// against the original payload when provided. requestedModel carries the client-visible
// model name before alias resolution so payload rules can target aliases precisely.
func applyPayloadConfigWithRoot(cfg *config.Config, model, protocol, root string, payload, original []byte, requestedModel string) []byte {
	if cfg == nil || len(payload) == 0 {
		return payload
	}
	rules := cfg.Payload
	if len(rules.Default) == 0 && len(rules.DefaultRaw) == 0 && len(rules.Override) == 0 && len(rules.OverrideRaw) == 0 && len(rules.Filter) == 0 {
		return payload
	}
	model = strings.TrimSpace(model)
	requestedModel = strings.TrimSpace(requestedModel)
	if model == "" && requestedModel == "" {
		return payload
	}
	candidates := payloadModelCandidates(model, requestedModel)
	out := payload
	source := original
	if len(source) == 0 {
		source = payload
	}
	appliedDefaults := make(map[string]struct{})
	// Apply default rules: first write wins per field across all matching rules.
	for i := range rules.Default {
		rule := &rules.Default[i]
		if !payloadModelRulesMatch(rule.Models, protocol, candidates) {
			continue
		}
		for path, value := range rule.Params {
			fullPath := buildPayloadPath(root, path)
			if fullPath == "" {
				continue
			}
			if gjson.GetBytes(source, fullPath).Exists() {
				continue
			}
			if _, ok := appliedDefaults[fullPath]; ok {
				continue
			}
			updated, errSet := sjson.SetBytes(out, fullPath, value)
			if errSet != nil {
				continue
			}
			out = updated
			appliedDefaults[fullPath] = struct{}{}
		}
	}
	// Apply default raw rules: first write wins per field across all matching rules.
	for i := range rules.DefaultRaw {
		rule := &rules.DefaultRaw[i]
		if !payloadModelRulesMatch(rule.Models, protocol, candidates) {
			continue
		}
		for path, value := range rule.Params {
			fullPath := buildPayloadPath(root, path)
			if fullPath == "" {
				continue
			}
			if gjson.GetBytes(source, fullPath).Exists() {
				continue
			}
			if _, ok := appliedDefaults[fullPath]; ok {
				continue
			}
			rawValue, ok := jsonutil.RawValue(value)
			if !ok {
				continue
			}
			updated, errSet := sjson.SetRawBytes(out, fullPath, rawValue)
			if errSet != nil {
				continue
			}
			out = updated
			appliedDefaults[fullPath] = struct{}{}
		}
	}
	// Apply override rules: last write wins per field across all matching rules.
	for i := range rules.Override {
		rule := &rules.Override[i]
		if !payloadModelRulesMatch(rule.Models, protocol, candidates) {
			continue
		}
		for path, value := range rule.Params {
			fullPath := buildPayloadPath(root, path)
			if fullPath == "" {
				continue
			}
			updated, errSet := sjson.SetBytes(out, fullPath, value)
			if errSet != nil {
				continue
			}
			out = updated
		}
	}
	// Apply override raw rules: last write wins per field across all matching rules.
	for i := range rules.OverrideRaw {
		rule := &rules.OverrideRaw[i]
		if !payloadModelRulesMatch(rule.Models, protocol, candidates) {
			continue
		}
		for path, value := range rule.Params {
			fullPath := buildPayloadPath(root, path)
			if fullPath == "" {
				continue
			}
			rawValue, ok := jsonutil.RawValue(value)
			if !ok {
				continue
			}
			updated, errSet := sjson.SetRawBytes(out, fullPath, rawValue)
			if errSet != nil {
				continue
			}
			out = updated
		}
	}
	// Apply filter rules: remove matching paths from payload.
	for i := range rules.Filter {
		rule := &rules.Filter[i]
		if !payloadModelRulesMatch(rule.Models, protocol, candidates) {
			continue
		}
		for _, path := range rule.Params {
			fullPath := buildPayloadPath(root, path)
			if fullPath == "" {
				continue
			}
			updated, errDel := sjson.DeleteBytes(out, fullPath)
			if errDel != nil {
				continue
			}
			out = updated
		}
	}
	return out
}

func payloadModelRulesMatch(rules []config.PayloadModelRule, protocol string, models []string) bool {
	if len(rules) == 0 || len(models) == 0 {
		return false
	}
	for _, model := range models {
		for _, entry := range rules {
			name := strings.TrimSpace(entry.Name)
			if name == "" {
				continue
			}
			if ep := strings.TrimSpace(entry.Protocol); ep != "" && protocol != "" && !strings.EqualFold(ep, protocol) {
				continue
			}
			if matchModelPattern(name, model) {
				return true
			}
		}
	}
	return false
}

func payloadModelCandidates(model, requestedModel string) []string {
	model = strings.TrimSpace(model)
	requestedModel = strings.TrimSpace(requestedModel)
	if model == "" && requestedModel == "" {
		return nil
	}
	candidates := make([]string, 0, 3)
	seen := make(map[string]struct{}, 3)
	addCandidate := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, value)
	}
	if model != "" {
		addCandidate(model)
	}
	if requestedModel != "" {
		parsed := thinking.ParseSuffix(requestedModel)
		base := strings.TrimSpace(parsed.ModelName)
		if base != "" {
			addCandidate(base)
		}
		if parsed.HasSuffix {
			addCandidate(requestedModel)
		}
	}
	return candidates
}

// buildPayloadPath combines an optional root path with a relative parameter path.
// When root is empty, the parameter path is used as-is. When root is non-empty,
// the parameter path is treated as relative to root.
func buildPayloadPath(root, path string) string {
	r := strings.TrimSpace(root)
	p := strings.TrimSpace(path)
	if r == "" {
		return p
	}
	if p == "" {
		return r
	}
	if strings.HasPrefix(p, ".") {
		p = p[1:]
	}
	return r + "." + p
}

func payloadRequestedModel(opts cliproxyexecutor.Options, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if len(opts.Metadata) == 0 {
		return fallback
	}
	raw, ok := opts.Metadata[cliproxyexecutor.RequestedModelMetadataKey]
	if !ok || raw == nil {
		return fallback
	}
	switch v := raw.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return fallback
		}
		return strings.TrimSpace(v)
	case []byte:
		if len(v) == 0 {
			return fallback
		}
		trimmed := strings.TrimSpace(string(v))
		if trimmed == "" {
			return fallback
		}
		return trimmed
	default:
		return fallback
	}
}

// matchModelPattern performs simple wildcard matching where '*' matches zero or more characters.
// Examples:
//
//	"*-5" matches "gpt-5"
//	"gpt-*" matches "gpt-5" and "gpt-4"
//	"gemini-*-pro" matches "gemini-2.5-pro" and "gemini-3-pro".
func matchModelPattern(pattern, model string) bool {
	pattern = strings.TrimSpace(pattern)
	model = strings.TrimSpace(model)
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	// Iterative glob-style matcher supporting only '*' wildcard.
	pi, si := 0, 0
	starIdx := -1
	matchIdx := 0
	for si < len(model) {
		if pi < len(pattern) && (pattern[pi] == model[si]) {
			pi++
			si++
			continue
		}
		if pi < len(pattern) && pattern[pi] == '*' {
			starIdx = pi
			matchIdx = si
			pi++
			continue
		}
		if starIdx != -1 {
			pi = starIdx + 1
			matchIdx++
			si = matchIdx
			continue
		}
		return false
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

// NormalizeThinkingConfig normalizes thinking-related fields in the payload
// based on model capabilities. For models without thinking support, it strips
// reasoning fields. For models with level-based thinking, it validates and
// normalizes the reasoning effort level. For models with numeric budget thinking,
// it strips the effort string fields.
func NormalizeThinkingConfig(payload []byte, model string, allowCompat bool) []byte {
	if len(payload) == 0 || model == "" {
		return payload
	}

	if !util.ModelSupportsThinking(model) {
		if allowCompat {
			return payload
		}
		return StripThinkingFields(payload, false)
	}

	if util.ModelUsesThinkingLevels(model) {
		return NormalizeReasoningEffortLevel(payload, model)
	}

	// Model supports thinking but uses numeric budgets, not levels.
	// Strip effort string fields since they are not applicable.
	return StripThinkingFields(payload, true)
}

// StripThinkingFields removes thinking-related fields from the payload for
// models that do not support thinking. If effortOnly is true, only removes
// effort string fields (for models using numeric budgets).
func StripThinkingFields(payload []byte, effortOnly bool) []byte {
	fieldsToRemove := []string{
		"reasoning_effort",
		"reasoning.effort",
	}
	if !effortOnly {
		fieldsToRemove = append([]string{"reasoning", "thinking"}, fieldsToRemove...)
	}
	out := payload
	for _, field := range fieldsToRemove {
		if gjson.GetBytes(out, field).Exists() {
			out, _ = sjson.DeleteBytes(out, field)
		}
	}
	return out
}

// NormalizeReasoningEffortLevel validates and normalizes the reasoning_effort
// or reasoning.effort field for level-based thinking models.
func NormalizeReasoningEffortLevel(payload []byte, model string) []byte {
	out := payload

	if effort := gjson.GetBytes(out, "reasoning_effort"); effort.Exists() {
		if normalized, ok := util.NormalizeReasoningEffortLevel(model, effort.String()); ok {
			out, _ = sjson.SetBytes(out, "reasoning_effort", normalized)
		}
	}

	if effort := gjson.GetBytes(out, "reasoning.effort"); effort.Exists() {
		if normalized, ok := util.NormalizeReasoningEffortLevel(model, effort.String()); ok {
			out, _ = sjson.SetBytes(out, "reasoning.effort", normalized)
		}
	}

	return out
}

// ValidateThinkingConfig checks for unsupported reasoning levels on level-based models.
// Returns a statusErr with 400 when an unsupported level is supplied to avoid silently
// downgrading requests.
func ValidateThinkingConfig(payload []byte, model string) error {
	if len(payload) == 0 || model == "" {
		return nil
	}
	if !util.ModelSupportsThinking(model) || !util.ModelUsesThinkingLevels(model) {
		return nil
	}

	levels := util.GetModelThinkingLevels(model)
	checkField := func(path string) error {
		if effort := gjson.GetBytes(payload, path); effort.Exists() {
			if _, ok := util.NormalizeReasoningEffortLevel(model, effort.String()); !ok {
				return statusErr{
					code: http.StatusBadRequest,
					msg:  fmt.Sprintf("unsupported reasoning effort level %q for model %s (supported: %s)", effort.String(), model, strings.Join(levels, ", ")),
				}
			}
		}
		return nil
	}

	if err := checkField("reasoning_effort"); err != nil {
		return err
	}
	if err := checkField("reasoning.effort"); err != nil {
		return err
	}
	return nil
}
