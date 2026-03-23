// Package gemini provides request translation functionality for Gemini CLI to Gemini API compatibility.
// It handles parsing and transforming Gemini CLI API requests into Gemini API format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package performs JSON data transformation to ensure compatibility
// between Gemini CLI API format and Gemini API's expected format.
package gemini

import (
	"fmt"
	"strings"

	"github.com/shariqriazz/modelgate/internal/translator/gemini/common"
	"github.com/shariqriazz/modelgate/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertGeminiRequestToAntigravity parses and transforms a Gemini CLI API request into Gemini API format.
func ConvertGeminiRequestToAntigravity(modelName string, inputRawJSON []byte, _ bool) []byte {
	rawJSON := inputRawJSON
	template := `{"project":"","request":{},"model":""}`
	template, _ = sjson.SetRaw(template, "request", string(rawJSON))
	template, _ = sjson.Set(template, "model", modelName)
	template, _ = sjson.Delete(template, "request.model")

	template, errFixCLIToolResponse := fixCLIToolResponse(template)
	if errFixCLIToolResponse != nil {
		return []byte{}
	}

	systemInstructionResult := gjson.Get(template, "request.system_instruction")
	if systemInstructionResult.Exists() {
		template, _ = sjson.SetRaw(template, "request.systemInstruction", systemInstructionResult.Raw)
		template, _ = sjson.Delete(template, "request.system_instruction")
	}
	rawJSON = []byte(template)

	// Normalize roles in request.contents: default to valid values if missing/invalid
	contents := gjson.GetBytes(rawJSON, "request.contents")
	if contents.Exists() {
		prevRole := ""
		idx := 0
		contents.ForEach(func(_ gjson.Result, value gjson.Result) bool {
			role := value.Get("role").String()
			valid := role == "user" || role == "model"
			if role == "" || !valid {
				var newRole string
				if prevRole == "" {
					newRole = "user"
				} else if prevRole == "user" {
					newRole = "model"
				} else {
					newRole = "user"
				}
				path := fmt.Sprintf("request.contents.%d.role", idx)
				rawJSON, _ = sjson.SetBytes(rawJSON, path, newRole)
				role = newRole
			}
			prevRole = role
			idx++
			return true
		})
	}

	toolsResult := gjson.GetBytes(rawJSON, "request.tools")
	if toolsResult.Exists() && toolsResult.IsArray() {
		toolResults := toolsResult.Array()
		for i := 0; i < len(toolResults); i++ {
			functionDeclarationsResult := gjson.GetBytes(rawJSON, fmt.Sprintf("request.tools.%d.function_declarations", i))
			if functionDeclarationsResult.Exists() && functionDeclarationsResult.IsArray() {
				functionDeclarationsResults := functionDeclarationsResult.Array()
				for j := 0; j < len(functionDeclarationsResults); j++ {
					parametersResult := gjson.GetBytes(rawJSON, fmt.Sprintf("request.tools.%d.function_declarations.%d.parameters", i, j))
					if parametersResult.Exists() {
						strJson, _ := util.RenameKey(string(rawJSON), fmt.Sprintf("request.tools.%d.function_declarations.%d.parameters", i, j), fmt.Sprintf("request.tools.%d.function_declarations.%d.parametersJsonSchema", i, j))
						rawJSON = []byte(strJson)
					}
				}
			}
		}
	}

	// Gemini-specific handling for non-Claude models:
	// - Add skip_thought_signature_validator to functionCall parts so upstream can bypass signature validation.
	// - Also mark thinking parts with the same sentinel when present (keep parts; only annotate them).
	if !strings.Contains(modelName, "claude") {
		const skipSentinel = "skip_thought_signature_validator"

		gjson.GetBytes(rawJSON, "request.contents").ForEach(func(contentIdx, content gjson.Result) bool {
			if content.Get("role").String() == "model" {
				var thinkingIndicesToSkipSignature []int64
				content.Get("parts").ForEach(func(partIdx, part gjson.Result) bool {
					if part.Get("thought").Bool() {
						thinkingIndicesToSkipSignature = append(thinkingIndicesToSkipSignature, partIdx.Int())
					}
					if part.Get("functionCall").Exists() {
						existingSig := part.Get("thoughtSignature").String()
						if existingSig == "" || len(existingSig) < 50 {
							rawJSON, _ = sjson.SetBytes(rawJSON, fmt.Sprintf("request.contents.%d.parts.%d.thoughtSignature", contentIdx.Int(), partIdx.Int()), skipSentinel)
						}
					}
					return true
				})

				for i := len(thinkingIndicesToSkipSignature) - 1; i >= 0; i-- {
					idx := thinkingIndicesToSkipSignature[i]
					rawJSON, _ = sjson.SetBytes(rawJSON, fmt.Sprintf("request.contents.%d.parts.%d.thoughtSignature", contentIdx.Int(), idx), skipSentinel)
				}
			}
			return true
		})
	} else {
		// Claude-specific: remove thinking blocks entirely and add skip sentinel to functionCall parts
		const skipSentinel = "skip_thought_signature_validator"

		gjson.GetBytes(rawJSON, "request.contents").ForEach(func(contentIdx, content gjson.Result) bool {
			if content.Get("role").String() == "model" {
				var thinkingIndicesToRemove []int64
				content.Get("parts").ForEach(func(partIdx, part gjson.Result) bool {
					if part.Get("thought").Bool() {
						thinkingIndicesToRemove = append(thinkingIndicesToRemove, partIdx.Int())
					}
					if part.Get("functionCall").Exists() {
						existingSig := part.Get("thoughtSignature").String()
						if existingSig == "" || len(existingSig) < 50 {
							rawJSON, _ = sjson.SetBytes(rawJSON, fmt.Sprintf("request.contents.%d.parts.%d.thoughtSignature", contentIdx.Int(), partIdx.Int()), skipSentinel)
						}
					}
					return true
				})

				for i := len(thinkingIndicesToRemove) - 1; i >= 0; i-- {
					idx := thinkingIndicesToRemove[i]
					rawJSON, _ = sjson.DeleteBytes(rawJSON, fmt.Sprintf("request.contents.%d.parts.%d", contentIdx.Int(), idx))
				}
			}
			return true
		})
	}

	return common.AttachDefaultSafetySettings(rawJSON, "request.safetySettings")
}

// FunctionCallGroup represents a group of function calls and their responses
type FunctionCallGroup struct {
	ResponsesNeeded int
	CallNames       []string // ordered function call names for backfilling empty response names
}

// parseFunctionResponseRaw attempts to normalize a function response part into a JSON object string.
// Falls back to a minimal "functionResponse" object when parsing fails.
// fallbackName is used when the response's own name is empty.
func parseFunctionResponseRaw(response gjson.Result, fallbackName string) string {
	if response.IsObject() && gjson.Valid(response.Raw) {
		raw := response.Raw
		name := response.Get("functionResponse.name").String()
		if strings.TrimSpace(name) == "" && fallbackName != "" {
			raw, _ = sjson.Set(raw, "functionResponse.name", fallbackName)
		}
		return raw
	}

	log.Debugf("parse function response failed, using fallback")
	funcResp := response.Get("functionResponse")
	if funcResp.Exists() {
		fr := `{"functionResponse":{"name":"","response":{"result":""}}}`
		name := funcResp.Get("name").String()
		if strings.TrimSpace(name) == "" {
			name = fallbackName
		}
		fr, _ = sjson.Set(fr, "functionResponse.name", name)
		fr, _ = sjson.Set(fr, "functionResponse.response.result", funcResp.Get("response").String())
		if id := funcResp.Get("id").String(); id != "" {
			fr, _ = sjson.Set(fr, "functionResponse.id", id)
		}
		return fr
	}

	useName := fallbackName
	if useName == "" {
		useName = "unknown"
	}
	fr := `{"functionResponse":{"name":"","response":{"result":""}}}`
	fr, _ = sjson.Set(fr, "functionResponse.name", useName)
	fr, _ = sjson.Set(fr, "functionResponse.response.result", response.String())
	return fr
}

// fixCLIToolResponse performs sophisticated tool response format conversion and grouping.
func fixCLIToolResponse(input string) (string, error) {
	parsed := gjson.Parse(input)
	contents := parsed.Get("request.contents")
	if !contents.Exists() {
		return input, fmt.Errorf("contents not found in input")
	}

	contentsWrapper := `{"contents":[]}`
	var pendingGroups []*FunctionCallGroup
	var collectedResponses []gjson.Result

	contents.ForEach(func(key, value gjson.Result) bool {
		role := value.Get("role").String()
		parts := value.Get("parts")

		var responsePartsInThisContent []gjson.Result
		parts.ForEach(func(_, part gjson.Result) bool {
			if part.Get("functionResponse").Exists() {
				responsePartsInThisContent = append(responsePartsInThisContent, part)
			}
			return true
		})

		if len(responsePartsInThisContent) > 0 {
			collectedResponses = append(collectedResponses, responsePartsInThisContent...)

			// Check if pending groups can be satisfied (FIFO: oldest group first)
			for len(pendingGroups) > 0 && len(collectedResponses) >= pendingGroups[0].ResponsesNeeded {
				group := pendingGroups[0]
				pendingGroups = pendingGroups[1:]

				groupResponses := collectedResponses[:group.ResponsesNeeded]
				collectedResponses = collectedResponses[group.ResponsesNeeded:]

				functionResponseContent := `{"parts":[],"role":"function"}`
				for ri, response := range groupResponses {
					fallbackName := ""
					if ri < len(group.CallNames) {
						fallbackName = group.CallNames[ri]
					}
					partRaw := parseFunctionResponseRaw(response, fallbackName)
					if partRaw != "" {
						functionResponseContent, _ = sjson.SetRaw(functionResponseContent, "parts.-1", partRaw)
					}
				}

				if gjson.Get(functionResponseContent, "parts.#").Int() > 0 {
					contentsWrapper, _ = sjson.SetRaw(contentsWrapper, "contents.-1", functionResponseContent)
				}
			}

			return true
		}

		if role == "model" {
			var callNames []string
			parts.ForEach(func(_, part gjson.Result) bool {
				if part.Get("functionCall").Exists() {
					callNames = append(callNames, part.Get("functionCall.name").String())
				}
				return true
			})

			if len(callNames) > 0 {
				if !value.IsObject() {
					log.Warnf("failed to parse model content")
					return true
				}
				contentsWrapper, _ = sjson.SetRaw(contentsWrapper, "contents.-1", value.Raw)

				group := &FunctionCallGroup{
					ResponsesNeeded: len(callNames),
					CallNames:       callNames,
				}
				pendingGroups = append(pendingGroups, group)
			} else {
				if !value.IsObject() {
					log.Warnf("failed to parse content")
					return true
				}
				contentsWrapper, _ = sjson.SetRaw(contentsWrapper, "contents.-1", value.Raw)
			}
		} else {
			if !value.IsObject() {
				log.Warnf("failed to parse content")
				return true
			}
			contentsWrapper, _ = sjson.SetRaw(contentsWrapper, "contents.-1", value.Raw)
		}

		return true
	})

	// Handle any remaining pending groups with remaining responses
	for _, group := range pendingGroups {
		if len(collectedResponses) >= group.ResponsesNeeded {
			groupResponses := collectedResponses[:group.ResponsesNeeded]
			collectedResponses = collectedResponses[group.ResponsesNeeded:]

			functionResponseContent := `{"parts":[],"role":"function"}`
			for ri, response := range groupResponses {
				fallbackName := ""
				if ri < len(group.CallNames) {
					fallbackName = group.CallNames[ri]
				}
				partRaw := parseFunctionResponseRaw(response, fallbackName)
				if partRaw != "" {
					functionResponseContent, _ = sjson.SetRaw(functionResponseContent, "parts.-1", partRaw)
				}
			}

			if gjson.Get(functionResponseContent, "parts.#").Int() > 0 {
				contentsWrapper, _ = sjson.SetRaw(contentsWrapper, "contents.-1", functionResponseContent)
			}
		}
	}

	result := input
	result, _ = sjson.SetRaw(result, "request.contents", gjson.Get(contentsWrapper, "contents").Raw)

	return result, nil
}
