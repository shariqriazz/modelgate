// Package claude provides request translation functionality for Claude Code API compatibility.
// This package handles the conversion of Claude Code API requests into Gemini CLI-compatible
// JSON format, transforming message contents, system instructions, and tool declarations
// into the format expected by Gemini CLI API clients. It performs JSON data transformation
// to ensure compatibility between Claude Code API format and Gemini CLI API's expected format.
package claude

import (
	"strings"

	"github.com/shariqriazz/modelgate/internal/cache"
	"github.com/shariqriazz/modelgate/internal/thinking"
	"github.com/shariqriazz/modelgate/internal/translator/gemini/common"
	"github.com/shariqriazz/modelgate/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertClaudeRequestToAntigravity parses and transforms a Claude Code API request into Gemini CLI API format.
func ConvertClaudeRequestToAntigravity(modelName string, inputRawJSON []byte, _ bool) []byte {
	enableThoughtTranslate := true
	rawJSON := inputRawJSON

	// system instruction
	systemInstructionJSON := ""
	hasSystemInstruction := false
	systemResult := gjson.GetBytes(rawJSON, "system")
	if systemResult.IsArray() {
		systemResults := systemResult.Array()
		systemInstructionJSON = `{"role":"user","parts":[]}`
		for i := 0; i < len(systemResults); i++ {
			systemPromptResult := systemResults[i]
			systemTypePromptResult := systemPromptResult.Get("type")
			if systemTypePromptResult.Type == gjson.String && systemTypePromptResult.String() == "text" {
				systemPrompt := systemPromptResult.Get("text").String()
				partJSON := `{}`
				if systemPrompt != "" {
					partJSON, _ = sjson.Set(partJSON, "text", systemPrompt)
				}
				systemInstructionJSON, _ = sjson.SetRaw(systemInstructionJSON, "parts.-1", partJSON)
				hasSystemInstruction = true
			}
		}
	} else if systemResult.Type == gjson.String {
		systemInstructionJSON = `{"role":"user","parts":[{"text":""}]}`
		systemInstructionJSON, _ = sjson.Set(systemInstructionJSON, "parts.0.text", systemResult.String())
		hasSystemInstruction = true
	}

	// contents
	contentsJSON := "[]"
	hasContents := false

	// tool_use_id → tool_name lookup, populated incrementally during the main loop.
	toolNameByID := make(map[string]string)

	messagesResult := gjson.GetBytes(rawJSON, "messages")
	if messagesResult.IsArray() {
		messageResults := messagesResult.Array()
		numMessages := len(messageResults)
		for i := 0; i < numMessages; i++ {
			messageResult := messageResults[i]
			roleResult := messageResult.Get("role")
			if roleResult.Type != gjson.String {
				continue
			}
			originalRole := roleResult.String()
			role := originalRole
			if role == "assistant" {
				role = "model"
			}
			clientContentJSON := `{"role":"","parts":[]}`
			clientContentJSON, _ = sjson.Set(clientContentJSON, "role", role)
			contentsResult := messageResult.Get("content")
			if contentsResult.IsArray() {
				contentResults := contentsResult.Array()
				numContents := len(contentResults)
				var currentMessageThinkingSignature string
				for j := 0; j < numContents; j++ {
					contentResult := contentResults[j]
					contentTypeResult := contentResult.Get("type")
					if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "thinking" {
						// Use GetThinkingText to handle wrapped thinking objects
						thinkingText := thinking.GetThinkingText(contentResult)

						// Always try cached signature first (more reliable than client-provided)
						signature := ""
						if thinkingText != "" {
							if cachedSig := cache.GetCachedSignature(modelName, thinkingText); cachedSig != "" {
								signature = cachedSig
							}
						}

						// Fallback to client signature only if cache miss and client signature is valid
						if signature == "" {
							signatureResult := contentResult.Get("signature")
							clientSignature := ""
							if signatureResult.Exists() && signatureResult.String() != "" {
								// Parse model-group prefixed signatures (e.g., "claude#<sig>")
								arrayClientSignatures := strings.SplitN(signatureResult.String(), "#", 2)
								if len(arrayClientSignatures) == 2 {
									if cache.GetModelGroup(modelName) == arrayClientSignatures[0] {
										clientSignature = arrayClientSignatures[1]
									}
								}
							}
							if cache.HasValidSignature(modelName, clientSignature) {
								signature = clientSignature
							}
						}

						// Store for subsequent tool_use in the same message
						if cache.HasValidSignature(modelName, signature) {
							currentMessageThinkingSignature = signature
						}

						// Skip trailing unsigned thinking blocks
						isUnsigned := !cache.HasValidSignature(modelName, signature)
						if isUnsigned {
							enableThoughtTranslate = false
							continue
						}

						// Valid signature, send as thought block
						partJSON := `{}`
						partJSON, _ = sjson.Set(partJSON, "thought", true)
						partJSON, _ = sjson.Set(partJSON, "text", thinkingText)
						if signature != "" {
							partJSON, _ = sjson.Set(partJSON, "thoughtSignature", signature)
						}
						clientContentJSON, _ = sjson.SetRaw(clientContentJSON, "parts.-1", partJSON)
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "text" {
						prompt := contentResult.Get("text").String()
						// Skip empty text parts to avoid Gemini API error
						if prompt == "" {
							continue
						}
						partJSON := `{}`
						partJSON, _ = sjson.Set(partJSON, "text", prompt)
						clientContentJSON, _ = sjson.SetRaw(clientContentJSON, "parts.-1", partJSON)
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "tool_use" {
						functionName := util.SanitizeFunctionName(contentResult.Get("name").String())
						argsResult := contentResult.Get("input")
						functionID := contentResult.Get("id").String()

						if functionID != "" && functionName != "" {
							toolNameByID[functionID] = functionName
						}

						// Handle both object and string input formats
						var argsRaw string
						if argsResult.IsObject() {
							argsRaw = argsResult.Raw
						} else if argsResult.Type == gjson.String {
							parsed := gjson.Parse(argsResult.String())
							if parsed.IsObject() {
								argsRaw = parsed.Raw
							}
						}

						if argsRaw != "" {
							partJSON := `{}`

							const skipSentinel = "skip_thought_signature_validator"
							if cache.HasValidSignature(modelName, currentMessageThinkingSignature) {
								partJSON, _ = sjson.Set(partJSON, "thoughtSignature", currentMessageThinkingSignature)
							} else {
								partJSON, _ = sjson.Set(partJSON, "thoughtSignature", skipSentinel)
							}

							if functionID != "" {
								partJSON, _ = sjson.Set(partJSON, "functionCall.id", functionID)
							}
							partJSON, _ = sjson.Set(partJSON, "functionCall.name", functionName)
							partJSON, _ = sjson.SetRaw(partJSON, "functionCall.args", argsRaw)
							clientContentJSON, _ = sjson.SetRaw(clientContentJSON, "parts.-1", partJSON)
						}
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "tool_result" {
						toolCallID := contentResult.Get("tool_use_id").String()
						if toolCallID != "" {
							funcName, ok := toolNameByID[toolCallID]
							if !ok {
								parts := strings.Split(toolCallID, "-")
								if len(parts) > 2 {
									funcName = strings.Join(parts[:len(parts)-2], "-")
								}
								if funcName == "" {
									funcName = toolCallID
								}
								log.Warnf("antigravity claude request: tool_result references unknown tool_use_id=%s, derived function name=%s", toolCallID, funcName)
							}
							functionResponseResult := contentResult.Get("content")

							functionResponseJSON := `{}`
							functionResponseJSON, _ = sjson.Set(functionResponseJSON, "id", toolCallID)
							functionResponseJSON, _ = sjson.Set(functionResponseJSON, "name", util.SanitizeFunctionName(funcName))

							if functionResponseResult.Type == gjson.String {
								functionResponseJSON, _ = sjson.Set(functionResponseJSON, "response.result", functionResponseResult.String())
							} else if functionResponseResult.IsArray() {
								frResults := functionResponseResult.Array()
								nonImageCount := 0
								lastNonImageRaw := ""
								filteredJSON := "[]"
								imagePartsJSON := "[]"
								for _, fr := range frResults {
									if fr.Get("type").String() == "image" && fr.Get("source.type").String() == "base64" {
										inlineDataJSON := `{}`
										if mimeType := fr.Get("source.media_type").String(); mimeType != "" {
											inlineDataJSON, _ = sjson.Set(inlineDataJSON, "mimeType", mimeType)
										}
										if data := fr.Get("source.data").String(); data != "" {
											inlineDataJSON, _ = sjson.Set(inlineDataJSON, "data", data)
										}
										imagePartJSON := `{}`
										imagePartJSON, _ = sjson.SetRaw(imagePartJSON, "inlineData", inlineDataJSON)
										imagePartsJSON, _ = sjson.SetRaw(imagePartsJSON, "-1", imagePartJSON)
										continue
									}
									nonImageCount++
									lastNonImageRaw = fr.Raw
									filteredJSON, _ = sjson.SetRaw(filteredJSON, "-1", fr.Raw)
								}

								if nonImageCount == 1 {
									functionResponseJSON, _ = sjson.SetRaw(functionResponseJSON, "response.result", lastNonImageRaw)
								} else if nonImageCount > 1 {
									functionResponseJSON, _ = sjson.SetRaw(functionResponseJSON, "response.result", filteredJSON)
								} else {
									functionResponseJSON, _ = sjson.Set(functionResponseJSON, "response.result", "")
								}

								if gjson.Get(imagePartsJSON, "#").Int() > 0 {
									functionResponseJSON, _ = sjson.SetRaw(functionResponseJSON, "parts", imagePartsJSON)
								}
							} else if functionResponseResult.IsObject() {
								if functionResponseResult.Get("type").String() == "image" && functionResponseResult.Get("source.type").String() == "base64" {
									inlineDataJSON := `{}`
									if mimeType := functionResponseResult.Get("source.media_type").String(); mimeType != "" {
										inlineDataJSON, _ = sjson.Set(inlineDataJSON, "mimeType", mimeType)
									}
									if data := functionResponseResult.Get("source.data").String(); data != "" {
										inlineDataJSON, _ = sjson.Set(inlineDataJSON, "data", data)
									}
									imagePartJSON := `{}`
									imagePartJSON, _ = sjson.SetRaw(imagePartJSON, "inlineData", inlineDataJSON)
									imagePartsJSON := "[]"
									imagePartsJSON, _ = sjson.SetRaw(imagePartsJSON, "-1", imagePartJSON)
									functionResponseJSON, _ = sjson.SetRaw(functionResponseJSON, "parts", imagePartsJSON)
									functionResponseJSON, _ = sjson.Set(functionResponseJSON, "response.result", "")
								} else {
									functionResponseJSON, _ = sjson.SetRaw(functionResponseJSON, "response.result", functionResponseResult.Raw)
								}
							} else if functionResponseResult.Raw != "" {
								functionResponseJSON, _ = sjson.SetRaw(functionResponseJSON, "response.result", functionResponseResult.Raw)
							} else {
								functionResponseJSON, _ = sjson.Set(functionResponseJSON, "response.result", "")
							}

							partJSON := `{}`
							partJSON, _ = sjson.SetRaw(partJSON, "functionResponse", functionResponseJSON)
							clientContentJSON, _ = sjson.SetRaw(clientContentJSON, "parts.-1", partJSON)
						}
					} else if contentTypeResult.Type == gjson.String && contentTypeResult.String() == "image" {
						sourceResult := contentResult.Get("source")
						if sourceResult.Get("type").String() == "base64" {
							inlineDataJSON := `{}`
							if mimeType := sourceResult.Get("media_type").String(); mimeType != "" {
								inlineDataJSON, _ = sjson.Set(inlineDataJSON, "mimeType", mimeType)
							}
							if data := sourceResult.Get("data").String(); data != "" {
								inlineDataJSON, _ = sjson.Set(inlineDataJSON, "data", data)
							}

							partJSON := `{}`
							partJSON, _ = sjson.SetRaw(partJSON, "inlineData", inlineDataJSON)
							clientContentJSON, _ = sjson.SetRaw(clientContentJSON, "parts.-1", partJSON)
						}
					}
				}

				// Reorder parts for 'model' role to ensure thinking block is first
				if role == "model" {
					partsResult := gjson.Get(clientContentJSON, "parts")
					if partsResult.IsArray() {
						parts := partsResult.Array()
						var thinkingParts []gjson.Result
						var otherParts []gjson.Result
						for _, part := range parts {
							if part.Get("thought").Bool() {
								thinkingParts = append(thinkingParts, part)
							} else {
								otherParts = append(otherParts, part)
							}
						}
						if len(thinkingParts) > 0 {
							firstPartIsThinking := parts[0].Get("thought").Bool()
							if !firstPartIsThinking || len(thinkingParts) > 1 {
								var newParts []interface{}
								for _, p := range thinkingParts {
									newParts = append(newParts, p.Value())
								}
								for _, p := range otherParts {
									newParts = append(newParts, p.Value())
								}
								clientContentJSON, _ = sjson.Set(clientContentJSON, "parts", newParts)
							}
						}
					}
				}

				// Skip messages with empty parts array to avoid Gemini API error
				partsCheck := gjson.Get(clientContentJSON, "parts")
				if !partsCheck.IsArray() || len(partsCheck.Array()) == 0 {
					continue
				}

				contentsJSON, _ = sjson.SetRaw(contentsJSON, "-1", clientContentJSON)
				hasContents = true
			} else if contentsResult.Type == gjson.String {
				prompt := contentsResult.String()
				partJSON := `{}`
				if prompt != "" {
					partJSON, _ = sjson.Set(partJSON, "text", prompt)
				}
				clientContentJSON, _ = sjson.SetRaw(clientContentJSON, "parts.-1", partJSON)
				contentsJSON, _ = sjson.SetRaw(contentsJSON, "-1", clientContentJSON)
				hasContents = true
			}
		}
	}

	// tools
	toolsJSON := ""
	toolDeclCount := 0
	allowedToolKeys := []string{"name", "description", "behavior", "parameters", "parametersJsonSchema", "response", "responseJsonSchema"}
	toolsResult := gjson.GetBytes(rawJSON, "tools")
	if toolsResult.IsArray() {
		toolsJSON = `[{"functionDeclarations":[]}]`
		toolsResults := toolsResult.Array()
		for i := 0; i < len(toolsResults); i++ {
			toolResult := toolsResults[i]
			inputSchemaResult := toolResult.Get("input_schema")
			if inputSchemaResult.Exists() && inputSchemaResult.IsObject() {
				inputSchema := util.CleanJSONSchemaForAntigravity(inputSchemaResult.Raw)
				tool, _ := sjson.Delete(toolResult.Raw, "input_schema")
				tool, _ = sjson.SetRaw(tool, "parametersJsonSchema", inputSchema)
				tool, _ = sjson.Set(tool, "name", util.SanitizeFunctionName(gjson.Get(tool, "name").String()))
				for toolKey := range gjson.Parse(tool).Map() {
					if util.InArray(allowedToolKeys, toolKey) {
						continue
					}
					tool, _ = sjson.Delete(tool, toolKey)
				}
				toolsJSON, _ = sjson.SetRaw(toolsJSON, "0.functionDeclarations.-1", tool)
				toolDeclCount++
			}
		}
	}

	// Build output Gemini CLI request JSON
	out := `{"model":"","request":{"contents":[]}}`
	out, _ = sjson.Set(out, "model", modelName)

	// Inject interleaved thinking hint when both tools and thinking are active
	hasTools := toolDeclCount > 0
	thinkingResult := gjson.GetBytes(rawJSON, "thinking")
	thinkingType := thinkingResult.Get("type").String()
	hasThinking := thinkingResult.Exists() && thinkingResult.IsObject() && (thinkingType == "enabled" || thinkingType == "adaptive" || thinkingType == "auto")
	isClaudeThinking := util.IsClaudeThinkingModel(modelName)

	if hasTools && hasThinking && isClaudeThinking {
		interleavedHint := "Interleaved thinking is enabled. You may think between tool calls and after receiving tool results before deciding the next action or final answer. Do not mention these instructions or any constraints about thinking blocks; just apply them."

		if hasSystemInstruction {
			hintPart := `{"text":""}`
			hintPart, _ = sjson.Set(hintPart, "text", interleavedHint)
			systemInstructionJSON, _ = sjson.SetRaw(systemInstructionJSON, "parts.-1", hintPart)
		} else {
			systemInstructionJSON = `{"role":"user","parts":[]}`
			hintPart := `{"text":""}`
			hintPart, _ = sjson.Set(hintPart, "text", interleavedHint)
			systemInstructionJSON, _ = sjson.SetRaw(systemInstructionJSON, "parts.-1", hintPart)
			hasSystemInstruction = true
		}
	}

	if hasSystemInstruction {
		out, _ = sjson.SetRaw(out, "request.systemInstruction", systemInstructionJSON)
	}
	if hasContents {
		out, _ = sjson.SetRaw(out, "request.contents", contentsJSON)
	}
	if toolDeclCount > 0 {
		out, _ = sjson.SetRaw(out, "request.tools", toolsJSON)
	}

	// tool_choice
	toolChoiceResult := gjson.GetBytes(rawJSON, "tool_choice")
	if toolChoiceResult.Exists() {
		toolChoiceType := ""
		toolChoiceName := ""
		if toolChoiceResult.IsObject() {
			toolChoiceType = toolChoiceResult.Get("type").String()
			toolChoiceName = toolChoiceResult.Get("name").String()
		} else if toolChoiceResult.Type == gjson.String {
			toolChoiceType = toolChoiceResult.String()
		}

		switch toolChoiceType {
		case "auto":
			out, _ = sjson.Set(out, "request.toolConfig.functionCallingConfig.mode", "AUTO")
		case "none":
			out, _ = sjson.Set(out, "request.toolConfig.functionCallingConfig.mode", "NONE")
		case "any":
			out, _ = sjson.Set(out, "request.toolConfig.functionCallingConfig.mode", "ANY")
		case "tool":
			out, _ = sjson.Set(out, "request.toolConfig.functionCallingConfig.mode", "ANY")
			if toolChoiceName != "" {
				out, _ = sjson.Set(out, "request.toolConfig.functionCallingConfig.allowedFunctionNames", []string{util.SanitizeFunctionName(toolChoiceName)})
			}
		}
	}

	// Map Anthropic thinking -> Gemini thinkingBudget/includeThoughts
	if t := gjson.GetBytes(rawJSON, "thinking"); enableThoughtTranslate && t.Exists() && t.IsObject() {
		switch t.Get("type").String() {
		case "enabled":
			if b := t.Get("budget_tokens"); b.Exists() && b.Type == gjson.Number {
				budget := int(b.Int())
				out, _ = sjson.Set(out, "request.generationConfig.thinkingConfig.thinkingBudget", budget)
				out, _ = sjson.Set(out, "request.generationConfig.thinkingConfig.includeThoughts", true)
			}
		case "adaptive", "auto":
			effort := ""
			if v := gjson.GetBytes(rawJSON, "output_config.effort"); v.Exists() && v.Type == gjson.String {
				effort = strings.ToLower(strings.TrimSpace(v.String()))
			}
			if effort != "" {
				out, _ = sjson.Set(out, "request.generationConfig.thinkingConfig.thinkingLevel", effort)
			} else {
				out, _ = sjson.Set(out, "request.generationConfig.thinkingConfig.thinkingLevel", "high")
			}
			out, _ = sjson.Set(out, "request.generationConfig.thinkingConfig.includeThoughts", true)
		}
	}
	if v := gjson.GetBytes(rawJSON, "temperature"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.Set(out, "request.generationConfig.temperature", v.Num)
	}
	if v := gjson.GetBytes(rawJSON, "top_p"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.Set(out, "request.generationConfig.topP", v.Num)
	}
	if v := gjson.GetBytes(rawJSON, "top_k"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.Set(out, "request.generationConfig.topK", v.Num)
	}
	if v := gjson.GetBytes(rawJSON, "max_tokens"); v.Exists() && v.Type == gjson.Number {
		out, _ = sjson.Set(out, "request.generationConfig.maxOutputTokens", v.Num)
	}

	outBytes := []byte(out)
	outBytes = common.AttachDefaultSafetySettings(outBytes, "request.safetySettings")

	return outBytes
}
