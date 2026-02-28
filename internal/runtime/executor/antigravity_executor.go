// Package executor provides runtime execution capabilities for various AI service providers.
// This file implements the Antigravity executor that proxies requests to the antigravity
// upstream using OAuth credentials.
package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shariqriazz/modelgate/internal/config"
	"github.com/shariqriazz/modelgate/internal/registry"
	"github.com/shariqriazz/modelgate/internal/util"
	sdkAuth "github.com/shariqriazz/modelgate/sdk/auth"
	modelgateauth "github.com/shariqriazz/modelgate/sdk/cliproxy/auth"
	modelgateexecutor "github.com/shariqriazz/modelgate/sdk/cliproxy/executor"
	sdktranslator "github.com/shariqriazz/modelgate/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	antigravityBaseURLDaily        = "https://daily-cloudcode-pa.googleapis.com"
	antigravitySandboxBaseURLDaily = "https://daily-cloudcode-pa.sandbox.googleapis.com"
	antigravityBaseURLProd         = "https://cloudcode-pa.googleapis.com"
	antigravityCountTokensPath     = "/v1internal:countTokens"
	antigravityStreamPath          = "/v1internal:streamGenerateContent"
	antigravityGeneratePath        = "/v1internal:generateContent"
	antigravityModelsPath          = "/v1internal:fetchAvailableModels"
	antigravityClientID            = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	antigravityClientSecret        = "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"
	defaultAntigravityAgent        = "antigravity/1.104.0 darwin/arm64"
	antigravityAuthType            = "antigravity"
	refreshSkew                    = 3000 * time.Second
	systemInstruction              = "You are Antigravity, a powerful agentic AI coding assistant designed by the Google Deepmind team working on Advanced Agentic Coding.You are pair programming with a USER to solve their coding task. The task may require creating a new codebase, modifying or debugging an existing codebase, or simply answering a question.**Absolute paths only****Proactiveness**"
)

var (
	randSource      = rand.New(rand.NewSource(time.Now().UnixNano()))
	randSourceMutex sync.Mutex
)

// AntigravityExecutor proxies requests to the antigravity upstream.
type AntigravityExecutor struct {
	cfg *config.Config
}

// NewAntigravityExecutor creates a new Antigravity executor instance.
//
// Parameters:
//   - cfg: The application configuration
//
// Returns:
//   - *AntigravityExecutor: A new Antigravity executor instance
func NewAntigravityExecutor(cfg *config.Config) *AntigravityExecutor {
	return &AntigravityExecutor{cfg: cfg}
}

// Identifier returns the executor identifier.
func (e *AntigravityExecutor) Identifier() string { return antigravityAuthType }

// PrepareRequest injects Antigravity credentials into the outgoing HTTP request.
func (e *AntigravityExecutor) PrepareRequest(req *http.Request, auth *modelgateauth.Auth) error {
	if req == nil {
		return nil
	}
	token, _, errToken := e.ensureAccessToken(req.Context(), auth)
	if errToken != nil {
		return errToken
	}
	if strings.TrimSpace(token) == "" {
		return statusErr{code: http.StatusUnauthorized, msg: "missing access token"}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// HttpRequest injects Antigravity credentials into the request and executes it.
func (e *AntigravityExecutor) HttpRequest(ctx context.Context, auth *modelgateauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("antigravity executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute performs a non-streaming request to the Antigravity API.
func (e *AntigravityExecutor) Execute(ctx context.Context, auth *modelgateauth.Auth, req modelgateexecutor.Request, opts modelgateexecutor.Options) (resp modelgateexecutor.Response, err error) {
	isClaude := strings.Contains(strings.ToLower(req.Model), "claude")
	if isClaude || strings.Contains(req.Model, "gemini-3-pro") || strings.Contains(req.Model, "gemini-3.1-pro") {
		return e.executeClaudeNonStream(ctx, auth, req, opts)
	}

	token, updatedAuth, errToken := e.ensureAccessToken(ctx, auth)
	if errToken != nil {
		return resp, errToken
	}
	if updatedAuth != nil {
		auth = updatedAuth
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("antigravity")
	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, req.Model, originalPayload, false)
	translated := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)

	translated = ApplyThinkingMetadataCLI(translated, req.Metadata, req.Model)
	translated = util.ApplyGemini3ThinkingLevelFromMetadataCLI(req.Model, req.Metadata, translated)
	translated = util.ApplyDefaultThinkingIfNeededCLI(req.Model, req.Metadata, translated)
	translated = normalizeAntigravityThinking(req.Model, translated, isClaude)
	requestedModel := payloadRequestedModel(opts, req.Model)
	translated = applyPayloadConfigWithRoot(e.cfg, req.Model, "antigravity", "request", translated, originalTranslated, requestedModel)

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)

	var lastStatus int
	var lastBody []byte
	var lastErr error

	for idx, baseURL := range baseURLs {
		httpReq, errReq := e.buildRequest(ctx, auth, token, req.Model, translated, false, opts.Alt, baseURL)
		if errReq != nil {
			err = errReq
			return resp, err
		}

		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			recordAPIResponseError(ctx, e.cfg, errDo)
			if errors.Is(errDo, context.Canceled) || errors.Is(errDo, context.DeadlineExceeded) {
				return resp, errDo
			}
			lastStatus = 0
			lastBody = nil
			lastErr = errDo
			if idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: request error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			err = errDo
			return resp, err
		}

		recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		bodyBytes, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			recordAPIResponseError(ctx, e.cfg, errRead)
			err = errRead
			return resp, err
		}
		appendAPIResponseChunk(ctx, e.cfg, bodyBytes)

		if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
			log.Debugf("antigravity executor: upstream error status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), bodyBytes))
			lastStatus = httpResp.StatusCode
			lastBody = append([]byte(nil), bodyBytes...)
			lastErr = nil
			if httpResp.StatusCode == http.StatusTooManyRequests && idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: rate limited on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			sErr := statusErr{code: httpResp.StatusCode, msg: string(bodyBytes)}
			if httpResp.StatusCode == http.StatusTooManyRequests {
				if retryAfter, parseErr := parseRetryDelay(bodyBytes); parseErr == nil && retryAfter != nil {
					sErr.retryAfter = retryAfter
				}
			}
			err = sErr
			return resp, err
		}

		reporter.publish(ctx, parseAntigravityUsage(bodyBytes))
		var param any
		converted := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), translated, bodyBytes, &param)
		resp = modelgateexecutor.Response{Payload: []byte(converted)}
		reporter.ensurePublished(ctx)
		return resp, nil
	}

	switch {
	case lastStatus != 0:
		sErr := statusErr{code: lastStatus, msg: string(lastBody)}
		if lastStatus == http.StatusTooManyRequests {
			if retryAfter, parseErr := parseRetryDelay(lastBody); parseErr == nil && retryAfter != nil {
				sErr.retryAfter = retryAfter
			}
		}
		err = sErr
	case lastErr != nil:
		err = lastErr
	default:
		err = statusErr{code: http.StatusServiceUnavailable, msg: "antigravity executor: no base url available"}
	}
	return resp, err
}

// executeClaudeNonStream performs a claude non-streaming request to the Antigravity API.
func (e *AntigravityExecutor) executeClaudeNonStream(ctx context.Context, auth *modelgateauth.Auth, req modelgateexecutor.Request, opts modelgateexecutor.Options) (resp modelgateexecutor.Response, err error) {
	token, updatedAuth, errToken := e.ensureAccessToken(ctx, auth)
	if errToken != nil {
		return resp, errToken
	}
	if updatedAuth != nil {
		auth = updatedAuth
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("antigravity")
	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, req.Model, originalPayload, true)
	translated := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)

	translated = ApplyThinkingMetadataCLI(translated, req.Metadata, req.Model)
	translated = util.ApplyGemini3ThinkingLevelFromMetadataCLI(req.Model, req.Metadata, translated)
	translated = util.ApplyDefaultThinkingIfNeededCLI(req.Model, req.Metadata, translated)
	translated = normalizeAntigravityThinking(req.Model, translated, true)
	requestedModel := payloadRequestedModel(opts, req.Model)
	translated = applyPayloadConfigWithRoot(e.cfg, req.Model, "antigravity", "request", translated, originalTranslated, requestedModel)

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)

	var lastStatus int
	var lastBody []byte
	var lastErr error

	for idx, baseURL := range baseURLs {
		httpReq, errReq := e.buildRequest(ctx, auth, token, req.Model, translated, true, opts.Alt, baseURL)
		if errReq != nil {
			err = errReq
			return resp, err
		}

		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			recordAPIResponseError(ctx, e.cfg, errDo)
			if errors.Is(errDo, context.Canceled) || errors.Is(errDo, context.DeadlineExceeded) {
				return resp, errDo
			}
			lastStatus = 0
			lastBody = nil
			lastErr = errDo
			if idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: request error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			err = errDo
			return resp, err
		}
		recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
			bodyBytes, errRead := io.ReadAll(httpResp.Body)
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("antigravity executor: close response body error: %v", errClose)
			}
			if errRead != nil {
				recordAPIResponseError(ctx, e.cfg, errRead)
				if errors.Is(errRead, context.Canceled) || errors.Is(errRead, context.DeadlineExceeded) {
					err = errRead
					return resp, err
				}
				if errCtx := ctx.Err(); errCtx != nil {
					err = errCtx
					return resp, err
				}
				lastStatus = 0
				lastBody = nil
				lastErr = errRead
				if idx+1 < len(baseURLs) {
					log.Debugf("antigravity executor: read error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
					continue
				}
				err = errRead
				return resp, err
			}
			appendAPIResponseChunk(ctx, e.cfg, bodyBytes)
			lastStatus = httpResp.StatusCode
			lastBody = append([]byte(nil), bodyBytes...)
			lastErr = nil
			if httpResp.StatusCode == http.StatusTooManyRequests && idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: rate limited on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			sErr := statusErr{code: httpResp.StatusCode, msg: string(bodyBytes)}
			if httpResp.StatusCode == http.StatusTooManyRequests {
				if retryAfter, parseErr := parseRetryDelay(bodyBytes); parseErr == nil && retryAfter != nil {
					sErr.retryAfter = retryAfter
				}
			}
			err = sErr
			return resp, err
		}

		out := make(chan modelgateexecutor.StreamChunk)
		go func(resp *http.Response) {
			defer close(out)
			defer func() {
				if errClose := resp.Body.Close(); errClose != nil {
					log.Errorf("antigravity executor: close response body error: %v", errClose)
				}
			}()
			scanner := bufio.NewScanner(resp.Body)
			scanner.Buffer(nil, streamScannerBuffer)
			for scanner.Scan() {
				line := scanner.Bytes()
				appendAPIResponseChunk(ctx, e.cfg, line)

				// Filter usage metadata for all models
				// Only retain usage statistics in the terminal chunk
				line = FilterSSEUsageMetadata(line)

				payload := jsonPayload(line)
				if payload == nil {
					continue
				}

				if detail, ok := parseAntigravityStreamUsage(payload); ok {
					reporter.publish(ctx, detail)
				}

				out <- modelgateexecutor.StreamChunk{Payload: payload}
			}
			if errScan := scanner.Err(); errScan != nil {
				recordAPIResponseError(ctx, e.cfg, errScan)
				reporter.publishFailure(ctx)
				out <- modelgateexecutor.StreamChunk{Err: errScan}
			} else {
				reporter.ensurePublished(ctx)
			}
		}(httpResp)

		var buffer bytes.Buffer
		for chunk := range out {
			if chunk.Err != nil {
				return resp, chunk.Err
			}
			if len(chunk.Payload) > 0 {
				_, _ = buffer.Write(chunk.Payload)
				_, _ = buffer.Write([]byte("\n"))
			}
		}
		resp = modelgateexecutor.Response{Payload: e.convertStreamToNonStream(buffer.Bytes())}

		reporter.publish(ctx, parseAntigravityUsage(resp.Payload))
		var param any
		converted := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), translated, resp.Payload, &param)
		resp = modelgateexecutor.Response{Payload: []byte(converted)}
		reporter.ensurePublished(ctx)

		return resp, nil
	}

	switch {
	case lastStatus != 0:
		sErr := statusErr{code: lastStatus, msg: string(lastBody)}
		if lastStatus == http.StatusTooManyRequests {
			if retryAfter, parseErr := parseRetryDelay(lastBody); parseErr == nil && retryAfter != nil {
				sErr.retryAfter = retryAfter
			}
		}
		err = sErr
	case lastErr != nil:
		err = lastErr
	default:
		err = statusErr{code: http.StatusServiceUnavailable, msg: "antigravity executor: no base url available"}
	}
	return resp, err
}

func (e *AntigravityExecutor) convertStreamToNonStream(stream []byte) []byte {
	responseTemplate := ""
	var traceID string
	var finishReason string
	var modelVersion string
	var responseID string
	var role string
	var usageRaw string
	parts := make([]map[string]interface{}, 0)
	var pendingKind string
	var pendingText strings.Builder
	var pendingThoughtSig string

	flushPending := func() {
		if pendingKind == "" {
			return
		}
		text := pendingText.String()
		switch pendingKind {
		case "text":
			if strings.TrimSpace(text) == "" {
				pendingKind = ""
				pendingText.Reset()
				pendingThoughtSig = ""
				return
			}
			parts = append(parts, map[string]interface{}{"text": text})
		case "thought":
			if strings.TrimSpace(text) == "" && pendingThoughtSig == "" {
				pendingKind = ""
				pendingText.Reset()
				pendingThoughtSig = ""
				return
			}
			part := map[string]interface{}{"thought": true}
			part["text"] = text
			if pendingThoughtSig != "" {
				part["thoughtSignature"] = pendingThoughtSig
			}
			parts = append(parts, part)
		}
		pendingKind = ""
		pendingText.Reset()
		pendingThoughtSig = ""
	}

	normalizePart := func(partResult gjson.Result) map[string]interface{} {
		var m map[string]interface{}
		_ = json.Unmarshal([]byte(partResult.Raw), &m)
		if m == nil {
			m = map[string]interface{}{}
		}
		sig := partResult.Get("thoughtSignature").String()
		if sig == "" {
			sig = partResult.Get("thought_signature").String()
		}
		if sig != "" {
			m["thoughtSignature"] = sig
			delete(m, "thought_signature")
		}
		if inlineData, ok := m["inline_data"]; ok {
			m["inlineData"] = inlineData
			delete(m, "inline_data")
		}
		return m
	}

	for _, line := range bytes.Split(stream, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || !gjson.ValidBytes(trimmed) {
			continue
		}

		root := gjson.ParseBytes(trimmed)
		responseNode := root.Get("response")
		if !responseNode.Exists() {
			if root.Get("candidates").Exists() {
				responseNode = root
			} else {
				continue
			}
		}
		responseTemplate = responseNode.Raw

		if traceResult := root.Get("traceId"); traceResult.Exists() && traceResult.String() != "" {
			traceID = traceResult.String()
		}

		if roleResult := responseNode.Get("candidates.0.content.role"); roleResult.Exists() {
			role = roleResult.String()
		}

		if finishResult := responseNode.Get("candidates.0.finishReason"); finishResult.Exists() && finishResult.String() != "" {
			finishReason = finishResult.String()
		}

		if modelResult := responseNode.Get("modelVersion"); modelResult.Exists() && modelResult.String() != "" {
			modelVersion = modelResult.String()
		}
		if responseIDResult := responseNode.Get("responseId"); responseIDResult.Exists() && responseIDResult.String() != "" {
			responseID = responseIDResult.String()
		}
		if usageResult := responseNode.Get("usageMetadata"); usageResult.Exists() {
			usageRaw = usageResult.Raw
		} else if usageResult := root.Get("usageMetadata"); usageResult.Exists() {
			usageRaw = usageResult.Raw
		}

		if partsResult := responseNode.Get("candidates.0.content.parts"); partsResult.IsArray() {
			for _, part := range partsResult.Array() {
				hasFunctionCall := part.Get("functionCall").Exists()
				hasInlineData := part.Get("inlineData").Exists() || part.Get("inline_data").Exists()
				sig := part.Get("thoughtSignature").String()
				if sig == "" {
					sig = part.Get("thought_signature").String()
				}
				text := part.Get("text").String()
				thought := part.Get("thought").Bool()

				if hasFunctionCall || hasInlineData {
					flushPending()
					parts = append(parts, normalizePart(part))
					continue
				}

				if thought || part.Get("text").Exists() {
					kind := "text"
					if thought {
						kind = "thought"
					}
					if pendingKind != "" && pendingKind != kind {
						flushPending()
					}
					pendingKind = kind
					pendingText.WriteString(text)
					if kind == "thought" && sig != "" {
						pendingThoughtSig = sig
					}
					continue
				}

				flushPending()
				parts = append(parts, normalizePart(part))
			}
		}
	}
	flushPending()

	if responseTemplate == "" {
		responseTemplate = `{"candidates":[{"content":{"role":"model","parts":[]}}]}`
	}

	partsJSON, _ := json.Marshal(parts)
	responseTemplate, _ = sjson.SetRaw(responseTemplate, "candidates.0.content.parts", string(partsJSON))
	if role != "" {
		responseTemplate, _ = sjson.Set(responseTemplate, "candidates.0.content.role", role)
	}
	if finishReason != "" {
		responseTemplate, _ = sjson.Set(responseTemplate, "candidates.0.finishReason", finishReason)
	}
	if modelVersion != "" {
		responseTemplate, _ = sjson.Set(responseTemplate, "modelVersion", modelVersion)
	}
	if responseID != "" {
		responseTemplate, _ = sjson.Set(responseTemplate, "responseId", responseID)
	}
	if usageRaw != "" {
		responseTemplate, _ = sjson.SetRaw(responseTemplate, "usageMetadata", usageRaw)
	} else if !gjson.Get(responseTemplate, "usageMetadata").Exists() {
		responseTemplate, _ = sjson.Set(responseTemplate, "usageMetadata.promptTokenCount", 0)
		responseTemplate, _ = sjson.Set(responseTemplate, "usageMetadata.candidatesTokenCount", 0)
		responseTemplate, _ = sjson.Set(responseTemplate, "usageMetadata.totalTokenCount", 0)
	}

	output := `{"response":{},"traceId":""}`
	output, _ = sjson.SetRaw(output, "response", responseTemplate)
	if traceID != "" {
		output, _ = sjson.Set(output, "traceId", traceID)
	}
	return []byte(output)
}

// streamValidationResult holds the result of validating a stream's first chunks
type streamValidationResult struct {
	bufferedChunks []modelgateexecutor.StreamChunk
	scanner        *bufio.Scanner
	resp           *http.Response
	needsRetry     bool
	retryReason    string
	malformedJSON  string // Original malformed JSON for auto-fix attempt
	scanErr        error
	isEmpty        bool
}

// validateStreamStart reads initial chunks to detect empty responses or malformed function calls
// before returning control to the caller. This enables retry on these conditions.
func (e *AntigravityExecutor) validateStreamStart(
	ctx context.Context,
	resp *http.Response,
	to, from sdktranslator.Format,
	model string,
	originalRequest, translated []byte,
	param *any,
	reporter *usageReporter,
) streamValidationResult {
	result := streamValidationResult{
		resp:           resp,
		bufferedChunks: make([]modelgateexecutor.StreamChunk, 0, 8),
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(nil, streamScannerBuffer)
	result.scanner = scanner

	contentChunkCount := 0
	maxValidationChunks := 50 // Read up to 50 SSE lines before deciding

	for i := 0; i < maxValidationChunks && scanner.Scan(); i++ {
		line := scanner.Bytes()
		appendAPIResponseChunk(ctx, e.cfg, line)

		line = FilterSSEUsageMetadata(line)

		payload := jsonPayload(line)
		if payload == nil {
			continue
		}

		// Check for MALFORMED_FUNCTION_CALL - this is retryable with auto-fix
		if malformedMsg := checkForMalformedFunctionCall(payload); malformedMsg != "" {
			log.Warnf("antigravity executor: MALFORMED_FUNCTION_CALL detected, attempting auto-fix")
			result.malformedJSON = malformedMsg

			// Try to auto-fix the malformed JSON
			if fixed, ok := attemptJSONRepair(malformedMsg); ok {
				log.Infof("antigravity executor: successfully repaired malformed JSON")
				// Create a synthetic valid tool call response
				syntheticChunk := createRepairedToolCallChunk([]byte(fixed), model)
				if syntheticChunk != nil {
					result.bufferedChunks = append(result.bufferedChunks, modelgateexecutor.StreamChunk{Payload: syntheticChunk})
					contentChunkCount++
				}
			} else {
				// Auto-fix failed, mark for retry
				result.needsRetry = true
				result.retryReason = "MALFORMED_FUNCTION_CALL"
				return result
			}
			continue
		}

		if detail, ok := parseAntigravityStreamUsage(payload); ok {
			reporter.publish(ctx, detail)
		}

		chunks := sdktranslator.TranslateStream(ctx, to, from, model, bytes.Clone(originalRequest), bytes.Clone(translated), bytes.Clone(payload), param)
		for _, chunk := range chunks {
			result.bufferedChunks = append(result.bufferedChunks, modelgateexecutor.StreamChunk{Payload: []byte(chunk)})
			contentChunkCount++
		}

		// Once we have real content, validation is complete
		if contentChunkCount > 0 {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		result.scanErr = err
		return result
	}

	// If we read all validation chunks and got nothing, it's likely empty
	if contentChunkCount == 0 {
		result.isEmpty = true
		result.needsRetry = true
		result.retryReason = "empty_response"
	}

	return result
}

// createRepairedToolCallChunk creates a synthetic SSE chunk for a repaired tool call
func createRepairedToolCallChunk(repairedJSON []byte, model string) []byte {
	// Parse the repaired JSON to extract function name and arguments
	var parsed map[string]any
	if err := json.Unmarshal(repairedJSON, &parsed); err != nil {
		return nil
	}

	// Build an OpenAI-compatible tool call delta
	toolCall := map[string]any{
		"id":   "repaired_call_" + fmt.Sprintf("%d", time.Now().UnixNano()),
		"type": "function",
		"function": map[string]any{
			"name":      parsed["name"],
			"arguments": string(repairedJSON),
		},
	}

	delta := map[string]any{
		"role":       "assistant",
		"tool_calls": []any{toolCall},
	}

	chunk := map[string]any{
		"id":      "repaired_" + fmt.Sprintf("%d", time.Now().UnixNano()),
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"delta":         delta,
				"finish_reason": "tool_calls",
			},
		},
	}

	data, err := json.Marshal(chunk)
	if err != nil {
		return nil
	}

	return append([]byte("data: "), data...)
}

// ExecuteStream performs a streaming request to the Antigravity API.
// Uses buffered validation to enable retry on empty responses, bare 429s, and malformed function calls.
func (e *AntigravityExecutor) ExecuteStream(ctx context.Context, auth *modelgateauth.Auth, req modelgateexecutor.Request, opts modelgateexecutor.Options) (stream <-chan modelgateexecutor.StreamChunk, err error) {
	ctx = context.WithValue(ctx, "alt", "")

	token, updatedAuth, errToken := e.ensureAccessToken(ctx, auth)
	if errToken != nil {
		return nil, errToken
	}
	if updatedAuth != nil {
		auth = updatedAuth
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	isClaude := strings.Contains(strings.ToLower(req.Model), "claude")

	from := opts.SourceFormat
	to := sdktranslator.FromString("antigravity")
	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, req.Model, originalPayload, true)
	translated := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)

	translated = ApplyThinkingMetadataCLI(translated, req.Metadata, req.Model)
	translated = util.ApplyGemini3ThinkingLevelFromMetadataCLI(req.Model, req.Metadata, translated)
	translated = util.ApplyDefaultThinkingIfNeededCLI(req.Model, req.Metadata, translated)
	translated = normalizeAntigravityThinking(req.Model, translated, isClaude)
	requestedModel := payloadRequestedModel(opts, req.Model)
	translated = applyPayloadConfigWithRoot(e.cfg, req.Model, "antigravity", "request", translated, originalTranslated, requestedModel)

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)

	attempts := antigravityRetryAttempts(auth, e.cfg)

	// Outer retry loop for empty responses, bare 429s, and malformed function calls
attemptLoop:
	for attempt := 0; attempt < attempts; attempt++ {
		var lastStatus int
		var lastBody []byte
		var lastErr error

		for idx, baseURL := range baseURLs {
			httpReq, errReq := e.buildRequest(ctx, auth, token, req.Model, translated, true, opts.Alt, baseURL)
			if errReq != nil {
				err = errReq
				return nil, err
			}

			httpResp, errDo := httpClient.Do(httpReq)
			if errDo != nil {
				recordAPIResponseError(ctx, e.cfg, errDo)
				if errors.Is(errDo, context.Canceled) || errors.Is(errDo, context.DeadlineExceeded) {
					return nil, errDo
				}
				lastStatus = 0
				lastBody = nil
				lastErr = errDo
				if idx+1 < len(baseURLs) {
					log.Debugf("antigravity executor: request error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
					continue
				}
				err = errDo
				return nil, err
			}
			recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

			// Handle non-2xx responses
			if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
				bodyBytes, errRead := io.ReadAll(httpResp.Body)
				if errClose := httpResp.Body.Close(); errClose != nil {
					log.Errorf("antigravity executor: close response body error: %v", errClose)
				}
				if errRead != nil {
					recordAPIResponseError(ctx, e.cfg, errRead)
					if errors.Is(errRead, context.Canceled) || errors.Is(errRead, context.DeadlineExceeded) {
						err = errRead
						return nil, err
					}
					if errCtx := ctx.Err(); errCtx != nil {
						err = errCtx
						return nil, err
					}
					lastStatus = 0
					lastBody = nil
					lastErr = errRead
					if idx+1 < len(baseURLs) {
						log.Debugf("antigravity executor: read error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
						continue
					}
					err = errRead
					return nil, err
				}
				appendAPIResponseChunk(ctx, e.cfg, bodyBytes)
				lastStatus = httpResp.StatusCode
				lastBody = append([]byte(nil), bodyBytes...)
				lastErr = nil

				// Handle 429 responses
				if httpResp.StatusCode == http.StatusTooManyRequests {
					// Check for "bare" 429 (transient rate limit without retry-after)
					if isBare429(httpResp.StatusCode, bodyBytes) {
						if attempt < attempts-1 {
							delay := antigravityNoCapacityRetryDelay(attempt)
							log.Warnf("antigravity executor: bare 429 from %s, attempt %d/%d, retrying in %v...",
								req.Model, attempt+1, attempts, delay)
							if errWait := antigravityWait(ctx, delay); errWait != nil {
								return nil, errWait
							}
							continue attemptLoop // Break inner loop, continue outer retry
						}
					}
					// Check for "no capacity" message (specific error)
					if antigravityShouldRetryNoCapacity(httpResp.StatusCode, bodyBytes) {
						if idx+1 < len(baseURLs) {
							log.Debugf("antigravity executor: no capacity on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
							continue
						}
						if attempt < attempts-1 {
							delay := antigravityNoCapacityRetryDelay(attempt)
							log.Debugf("antigravity executor: no capacity for model %s, retrying in %s (attempt %d/%d)", req.Model, delay, attempt+1, attempts)
							if errWait := antigravityWait(ctx, delay); errWait != nil {
								return nil, errWait
							}
							continue attemptLoop
						}
					}
					if idx+1 < len(baseURLs) {
						log.Debugf("antigravity executor: rate limited on base url %s, retrying with fallback", baseURL)
						continue
					}
				}

				sErr := statusErr{code: httpResp.StatusCode, msg: string(bodyBytes)}
				if httpResp.StatusCode == http.StatusTooManyRequests {
					if retryAfter, parseErr := parseRetryDelay(bodyBytes); parseErr == nil && retryAfter != nil {
						sErr.retryAfter = retryAfter
					}
				}
				err = sErr
				return nil, err
			}

			// Success - validate stream start before returning channel
			var param any
			validation := e.validateStreamStart(ctx, httpResp, to, from, req.Model,
				bytes.Clone(opts.OriginalRequest), translated, &param, reporter)

			// Check if we need to retry (empty response or malformed call)
			if validation.needsRetry && attempt < attempts-1 {
				delay := antigravityNoCapacityRetryDelay(attempt)
				log.Warnf("antigravity executor: %s detected from %s, attempt %d/%d, retrying in %v...",
					validation.retryReason, req.Model, attempt+1, attempts, delay)
				if errClose := httpResp.Body.Close(); errClose != nil {
					log.Debugf("antigravity executor: close body on retry: %v", errClose)
				}
				if errWait := antigravityWait(ctx, delay); errWait != nil {
					return nil, errWait
				}
				continue attemptLoop // Break inner loop, continue outer retry
			}

			// Validation passed or max retries reached - return the stream
			out := make(chan modelgateexecutor.StreamChunk, len(validation.bufferedChunks)+16)
			stream = out

			go func(v streamValidationResult, attemptNum int) {
				defer close(out)
				defer func() {
					if errClose := v.resp.Body.Close(); errClose != nil {
						log.Errorf("antigravity executor: close response body error: %v", errClose)
					}
				}()

				// First, emit any buffered chunks from validation
				for _, chunk := range v.bufferedChunks {
					out <- chunk
				}

				chunkCount := len(v.bufferedChunks)

				// Continue reading remaining stream
				for v.scanner.Scan() {
					line := v.scanner.Bytes()
					appendAPIResponseChunk(ctx, e.cfg, line)

					line = FilterSSEUsageMetadata(line)

					payload := jsonPayload(line)
					if payload == nil {
						continue
					}

					// Check for MALFORMED_FUNCTION_CALL in remaining stream
					if malformedMsg := checkForMalformedFunctionCall(payload); malformedMsg != "" {
						log.Warnf("antigravity executor: MALFORMED_FUNCTION_CALL in stream: %s", malformedMsg[:min(100, len(malformedMsg))])
						if fixed, ok := attemptJSONRepair(malformedMsg); ok {
							log.Infof("antigravity executor: repaired malformed JSON in-stream")
							if syntheticChunk := createRepairedToolCallChunk([]byte(fixed), req.Model); syntheticChunk != nil {
								out <- modelgateexecutor.StreamChunk{Payload: syntheticChunk}
								chunkCount++
							}
						}
						continue
					}

					if detail, ok := parseAntigravityStreamUsage(payload); ok {
						reporter.publish(ctx, detail)
					}

					chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), translated, bytes.Clone(payload), &param)
					for i := range chunks {
						chunkCount++
						out <- modelgateexecutor.StreamChunk{Payload: []byte(chunks[i])}
					}
				}

				tail := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), translated, []byte("[DONE]"), &param)
				for i := range tail {
					chunkCount++
					out <- modelgateexecutor.StreamChunk{Payload: []byte(tail[i])}
				}

				if errScan := v.scanner.Err(); errScan != nil {
					recordAPIResponseError(ctx, e.cfg, errScan)
					reporter.publishFailure(ctx)
					out <- modelgateexecutor.StreamChunk{Err: errScan}
				} else {
					if chunkCount == 0 {
						log.Warnf("antigravity executor: stream completed with zero content chunks (attempt %d)", attemptNum+1)
					}
					reporter.ensurePublished(ctx)
				}
			}(validation, attempt)

			return stream, nil
		}

		// If bare 429 triggered break, continue outer loop (fallback to attemptLoop label)
		if lastStatus == http.StatusTooManyRequests && isBare429(lastStatus, lastBody) {
			continue
		}

		// Handle final errors
		switch {
		case lastStatus != 0:
			sErr := statusErr{code: lastStatus, msg: string(lastBody)}
			if lastStatus == http.StatusTooManyRequests {
				if retryAfter, parseErr := parseRetryDelay(lastBody); parseErr == nil && retryAfter != nil {
					sErr.retryAfter = retryAfter
				}
			}
			err = sErr
		case lastErr != nil:
			err = lastErr
		default:
			err = statusErr{code: http.StatusServiceUnavailable, msg: "antigravity executor: no base url available"}
		}
		return nil, err
	}

	err = statusErr{code: http.StatusServiceUnavailable, msg: "antigravity executor: max retry attempts exceeded"}
	return nil, err
}

// Refresh refreshes the authentication credentials using the refresh token.
func (e *AntigravityExecutor) Refresh(ctx context.Context, auth *modelgateauth.Auth) (*modelgateauth.Auth, error) {
	if auth == nil {
		return auth, nil
	}
	updated, errRefresh := e.refreshToken(ctx, auth.Clone())
	if errRefresh != nil {
		return nil, errRefresh
	}
	return updated, nil
}

// CountTokens counts tokens for the given request using the Antigravity API.
func (e *AntigravityExecutor) CountTokens(ctx context.Context, auth *modelgateauth.Auth, req modelgateexecutor.Request, opts modelgateexecutor.Options) (modelgateexecutor.Response, error) {
	token, updatedAuth, errToken := e.ensureAccessToken(ctx, auth)
	if errToken != nil {
		return modelgateexecutor.Response{}, errToken
	}
	if updatedAuth != nil {
		auth = updatedAuth
	}
	if strings.TrimSpace(token) == "" {
		return modelgateexecutor.Response{}, statusErr{code: http.StatusUnauthorized, msg: "missing access token"}
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("antigravity")
	respCtx := context.WithValue(ctx, "alt", opts.Alt)

	isClaude := strings.Contains(strings.ToLower(req.Model), "claude")

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}

	var lastStatus int
	var lastBody []byte
	var lastErr error

	for idx, baseURL := range baseURLs {
		payload := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)
		payload = ApplyThinkingMetadataCLI(payload, req.Metadata, req.Model)
		payload = util.ApplyDefaultThinkingIfNeededCLI(req.Model, req.Metadata, payload)
		payload = normalizeAntigravityThinking(req.Model, payload, isClaude)
		payload = deleteJSONField(payload, "project")
		payload = deleteJSONField(payload, "model")
		payload = deleteJSONField(payload, "request.safetySettings")

		base := strings.TrimSuffix(baseURL, "/")
		if base == "" {
			base = buildBaseURL(auth)
		}

		var requestURL strings.Builder
		requestURL.WriteString(base)
		requestURL.WriteString(antigravityCountTokensPath)
		if opts.Alt != "" {
			requestURL.WriteString("?$alt=")
			requestURL.WriteString(url.QueryEscape(opts.Alt))
		}

		httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bytes.NewReader(payload))
		if errReq != nil {
			return modelgateexecutor.Response{}, errReq
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+token)
		httpReq.Header.Set("User-Agent", resolveUserAgent(auth))
		httpReq.Header.Set("Accept", "application/json")
		if host := resolveHost(base); host != "" {
			httpReq.Host = host
		}

		recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
			URL:       requestURL.String(),
			Method:    http.MethodPost,
			Headers:   httpReq.Header.Clone(),
			Body:      payload,
			Provider:  e.Identifier(),
			AuthID:    authID,
			AuthLabel: authLabel,
			AuthType:  authType,
			AuthValue: authValue,
		})

		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			recordAPIResponseError(ctx, e.cfg, errDo)
			if errors.Is(errDo, context.Canceled) || errors.Is(errDo, context.DeadlineExceeded) {
				return modelgateexecutor.Response{}, errDo
			}
			lastStatus = 0
			lastBody = nil
			lastErr = errDo
			if idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: request error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			return modelgateexecutor.Response{}, errDo
		}

		recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
		bodyBytes, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			recordAPIResponseError(ctx, e.cfg, errRead)
			return modelgateexecutor.Response{}, errRead
		}
		appendAPIResponseChunk(ctx, e.cfg, bodyBytes)

		if httpResp.StatusCode >= http.StatusOK && httpResp.StatusCode < http.StatusMultipleChoices {
			count := gjson.GetBytes(bodyBytes, "totalTokens").Int()
			translated := sdktranslator.TranslateTokenCount(respCtx, to, from, count, bodyBytes)
			return modelgateexecutor.Response{Payload: []byte(translated)}, nil
		}

		lastStatus = httpResp.StatusCode
		lastBody = append([]byte(nil), bodyBytes...)
		lastErr = nil
		if httpResp.StatusCode == http.StatusTooManyRequests && idx+1 < len(baseURLs) {
			log.Debugf("antigravity executor: rate limited on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
			continue
		}
		sErr := statusErr{code: httpResp.StatusCode, msg: string(bodyBytes)}
		if httpResp.StatusCode == http.StatusTooManyRequests {
			if retryAfter, parseErr := parseRetryDelay(bodyBytes); parseErr == nil && retryAfter != nil {
				sErr.retryAfter = retryAfter
			}
		}
		return modelgateexecutor.Response{}, sErr
	}

	switch {
	case lastStatus != 0:
		sErr := statusErr{code: lastStatus, msg: string(lastBody)}
		if lastStatus == http.StatusTooManyRequests {
			if retryAfter, parseErr := parseRetryDelay(lastBody); parseErr == nil && retryAfter != nil {
				sErr.retryAfter = retryAfter
			}
		}
		return modelgateexecutor.Response{}, sErr
	case lastErr != nil:
		return modelgateexecutor.Response{}, lastErr
	default:
		return modelgateexecutor.Response{}, statusErr{code: http.StatusServiceUnavailable, msg: "antigravity executor: no base url available"}
	}
}

// FetchAntigravityModels retrieves available models using the supplied auth.
func FetchAntigravityModels(ctx context.Context, auth *modelgateauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	exec := &AntigravityExecutor{cfg: cfg}
	token, updatedAuth, errToken := exec.ensureAccessToken(ctx, auth)
	if errToken != nil {
		log.Warnf("antigravity executor: fetch models failed for %s: token error: %v", auth.ID, errToken)
		return nil
	}
	if token == "" {
		log.Warnf("antigravity executor: fetch models failed for %s: got empty token", auth.ID)
		return nil
	}
	if updatedAuth != nil {
		auth = updatedAuth
	}

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0)

	for idx, baseURL := range baseURLs {
		modelsURL := baseURL + antigravityModelsPath
		httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, modelsURL, bytes.NewReader([]byte(`{}`)))
		if errReq != nil {
			log.Warnf("antigravity executor: fetch models failed for %s: create request error: %v", auth.ID, errReq)
			return nil
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+token)
		httpReq.Header.Set("User-Agent", resolveUserAgent(auth))
		if host := resolveHost(baseURL); host != "" {
			httpReq.Host = host
		}

		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			if errors.Is(errDo, context.Canceled) || errors.Is(errDo, context.DeadlineExceeded) {
				log.Warnf("antigravity executor: fetch models failed for %s: context canceled: %v", auth.ID, errDo)
				return nil
			}
			if idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: models request error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			log.Warnf("antigravity executor: fetch models failed for %s: request error: %v", auth.ID, errDo)
			return nil
		}

		bodyBytes, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			if idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: models read error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			log.Warnf("antigravity executor: fetch models failed for %s: read body error: %v", auth.ID, errRead)
			return nil
		}
		if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
			if httpResp.StatusCode == http.StatusTooManyRequests && idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: models request rate limited on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			log.Warnf("antigravity executor: fetch models failed for %s: unexpected status %d, body: %s", auth.ID, httpResp.StatusCode, string(bodyBytes))
			return nil
		}

		result := gjson.GetBytes(bodyBytes, "models")
		if !result.Exists() {
			log.Warnf("antigravity executor: fetch models failed for %s: no models field in response, body: %s", auth.ID, string(bodyBytes))
			return nil
		}

		now := time.Now().Unix()
		modelConfig := registry.GetAntigravityModelConfig()
		models := make([]*registry.ModelInfo, 0, len(result.Map()))
		for originalName := range result.Map() {
			aliasName := modelName2Alias(originalName)
			if aliasName != "" {
				cfg := modelConfig[aliasName]
				modelName := aliasName
				if cfg != nil && cfg.Name != "" {
					modelName = cfg.Name
				}
				modelInfo := &registry.ModelInfo{
					ID:          aliasName,
					Name:        modelName,
					Description: aliasName,
					DisplayName: aliasName,
					Version:     aliasName,
					Object:      "model",
					Created:     now,
					OwnedBy:     antigravityAuthType,
					Type:        antigravityAuthType,
				}
				// Look up Thinking support from static config using alias name
				if cfg != nil {
					if cfg.Thinking != nil {
						modelInfo.Thinking = cfg.Thinking
					}
					if cfg.MaxCompletionTokens > 0 {
						modelInfo.MaxCompletionTokens = cfg.MaxCompletionTokens
					}
				}
				models = append(models, modelInfo)
			}
		}
		return models
	}
	return nil
}

func (e *AntigravityExecutor) ensureAccessToken(ctx context.Context, auth *modelgateauth.Auth) (string, *modelgateauth.Auth, error) {
	if auth == nil {
		return "", nil, statusErr{code: http.StatusUnauthorized, msg: "missing auth"}
	}
	accessToken := metaStringValue(auth.Metadata, "access_token")
	expiry := tokenExpiry(auth.Metadata)
	if accessToken != "" && expiry.After(time.Now().Add(refreshSkew)) {
		return accessToken, nil, nil
	}
	refreshCtx := context.Background()
	if ctx != nil {
		if rt, ok := ctx.Value("modelgate.roundtripper").(http.RoundTripper); ok && rt != nil {
			refreshCtx = context.WithValue(refreshCtx, "modelgate.roundtripper", rt)
		}
	}
	updated, errRefresh := e.refreshToken(refreshCtx, auth.Clone())
	if errRefresh != nil {
		return "", nil, errRefresh
	}
	return metaStringValue(updated.Metadata, "access_token"), updated, nil
}

func (e *AntigravityExecutor) refreshToken(ctx context.Context, auth *modelgateauth.Auth) (*modelgateauth.Auth, error) {
	if auth == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing auth"}
	}
	refreshToken := metaStringValue(auth.Metadata, "refresh_token")
	if refreshToken == "" {
		return auth, statusErr{code: http.StatusUnauthorized, msg: "missing refresh token"}
	}

	form := url.Values{}
	form.Set("client_id", antigravityClientID)
	form.Set("client_secret", antigravityClientSecret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if errReq != nil {
		return auth, errReq
	}
	httpReq.Header.Set("Host", "oauth2.googleapis.com")
	httpReq.Header.Set("User-Agent", defaultAntigravityAgent)
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		return auth, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
	}()

	bodyBytes, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		return auth, errRead
	}

	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		sErr := statusErr{code: httpResp.StatusCode, msg: string(bodyBytes)}
		if httpResp.StatusCode == http.StatusTooManyRequests {
			if retryAfter, parseErr := parseRetryDelay(bodyBytes); parseErr == nil && retryAfter != nil {
				sErr.retryAfter = retryAfter
			}
		}
		return auth, sErr
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if errUnmarshal := json.Unmarshal(bodyBytes, &tokenResp); errUnmarshal != nil {
		return auth, errUnmarshal
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		auth.Metadata["refresh_token"] = tokenResp.RefreshToken
	}
	auth.Metadata["expires_in"] = tokenResp.ExpiresIn
	now := time.Now()
	auth.Metadata["timestamp"] = now.UnixMilli()
	auth.Metadata["expired"] = now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339)
	auth.Metadata["type"] = antigravityAuthType
	if errProject := e.ensureAntigravityProjectID(ctx, auth, tokenResp.AccessToken); errProject != nil {
		log.Warnf("antigravity executor: ensure project id failed: %v", errProject)
	}
	return auth, nil
}

func (e *AntigravityExecutor) ensureAntigravityProjectID(ctx context.Context, auth *modelgateauth.Auth, accessToken string) error {
	if auth == nil {
		return nil
	}

	if auth.Metadata["project_id"] != nil {
		return nil
	}

	token := strings.TrimSpace(accessToken)
	if token == "" {
		token = metaStringValue(auth.Metadata, "access_token")
	}
	if token == "" {
		return nil
	}

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	projectID, errFetch := sdkAuth.FetchAntigravityProjectID(ctx, token, httpClient)
	if errFetch != nil {
		return errFetch
	}
	if strings.TrimSpace(projectID) == "" {
		return nil
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["project_id"] = strings.TrimSpace(projectID)

	return nil
}

func (e *AntigravityExecutor) buildRequest(ctx context.Context, auth *modelgateauth.Auth, token, modelName string, payload []byte, stream bool, alt, baseURL string) (*http.Request, error) {
	if token == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing access token"}
	}

	base := strings.TrimSuffix(baseURL, "/")
	if base == "" {
		base = buildBaseURL(auth)
	}
	path := antigravityGeneratePath
	if stream {
		path = antigravityStreamPath
	}
	var requestURL strings.Builder
	requestURL.WriteString(base)
	requestURL.WriteString(path)
	if stream {
		if alt != "" {
			requestURL.WriteString("?$alt=")
			requestURL.WriteString(url.QueryEscape(alt))
		} else {
			requestURL.WriteString("?alt=sse")
		}
	} else if alt != "" {
		requestURL.WriteString("?$alt=")
		requestURL.WriteString(url.QueryEscape(alt))
	}

	// Extract project_id from auth metadata if available
	projectID := ""
	if auth != nil && auth.Metadata != nil {
		if pid, ok := auth.Metadata["project_id"].(string); ok {
			projectID = strings.TrimSpace(pid)
		}
	}
	payload = geminiToAntigravity(modelName, payload, projectID)
	payload, _ = sjson.SetBytes(payload, "model", alias2ModelName(modelName))

	if strings.Contains(modelName, "claude") {
		strJSON := string(payload)
		paths := make([]string, 0)
		util.Walk(gjson.ParseBytes(payload), "", "parametersJsonSchema", &paths)
		for _, p := range paths {
			strJSON, _ = util.RenameKey(strJSON, p, p[:len(p)-len("parametersJsonSchema")]+"parameters")
		}

		// Use the centralized schema cleaner to handle unsupported keywords,
		// const->enum conversion, and flattening of types/anyOf.
		strJSON = util.CleanJSONSchemaForAntigravity(strJSON)

		payload = []byte(strJSON)
	}

	if strings.Contains(modelName, "claude") || strings.Contains(modelName, "gemini-3-pro-preview") || strings.Contains(modelName, "gemini-3.1-pro-preview") {
		systemInstructionPartsResult := gjson.GetBytes(payload, "request.systemInstruction.parts")
		payload, _ = sjson.SetBytes(payload, "request.systemInstruction.role", "user")
		payload, _ = sjson.SetBytes(payload, "request.systemInstruction.parts.0.text", systemInstruction)
		payload, _ = sjson.SetBytes(payload, "request.systemInstruction.parts.1.text", fmt.Sprintf("Please ignore following [ignore]%s[/ignore]", systemInstruction))

		if systemInstructionPartsResult.Exists() && systemInstructionPartsResult.IsArray() {
			for _, partResult := range systemInstructionPartsResult.Array() {
				payload, _ = sjson.SetRawBytes(payload, "request.systemInstruction.parts.-1", []byte(partResult.Raw))
			}
		}
	}

	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bytes.NewReader(payload))
	if errReq != nil {
		return nil, errReq
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("User-Agent", resolveUserAgent(auth))
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	if host := resolveHost(base); host != "" {
		httpReq.Host = host
	}

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       requestURL.String(),
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      payload,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	return httpReq, nil
}

func tokenExpiry(metadata map[string]any) time.Time {
	if metadata == nil {
		return time.Time{}
	}
	if expStr, ok := metadata["expired"].(string); ok {
		expStr = strings.TrimSpace(expStr)
		if expStr != "" {
			if parsed, errParse := time.Parse(time.RFC3339, expStr); errParse == nil {
				return parsed
			}
		}
	}
	expiresIn, hasExpires := int64Value(metadata["expires_in"])
	tsMs, hasTimestamp := int64Value(metadata["timestamp"])
	if hasExpires && hasTimestamp {
		return time.Unix(0, tsMs*int64(time.Millisecond)).Add(time.Duration(expiresIn) * time.Second)
	}
	return time.Time{}
}

func metaStringValue(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	if v, ok := metadata[key]; ok {
		switch typed := v.(type) {
		case string:
			return strings.TrimSpace(typed)
		case []byte:
			return strings.TrimSpace(string(typed))
		}
	}
	return ""
}

func int64Value(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	case json.Number:
		if i, errParse := typed.Int64(); errParse == nil {
			return i, true
		}
	case string:
		if strings.TrimSpace(typed) == "" {
			return 0, false
		}
		if i, errParse := strconv.ParseInt(strings.TrimSpace(typed), 10, 64); errParse == nil {
			return i, true
		}
	}
	return 0, false
}

func buildBaseURL(auth *modelgateauth.Auth) string {
	if baseURLs := antigravityBaseURLFallbackOrder(auth); len(baseURLs) > 0 {
		return baseURLs[0]
	}
	return antigravityBaseURLDaily
}

func resolveHost(base string) string {
	parsed, errParse := url.Parse(base)
	if errParse != nil {
		return ""
	}
	if parsed.Host != "" {
		return parsed.Host
	}
	return strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
}

func resolveUserAgent(auth *modelgateauth.Auth) string {
	if auth != nil {
		if auth.Attributes != nil {
			if ua := strings.TrimSpace(auth.Attributes["user_agent"]); ua != "" {
				return ua
			}
		}
		if auth.Metadata != nil {
			if ua, ok := auth.Metadata["user_agent"].(string); ok && strings.TrimSpace(ua) != "" {
				return strings.TrimSpace(ua)
			}
		}
	}
	return defaultAntigravityAgent
}

func antigravityRetryAttempts(auth *modelgateauth.Auth, cfg *config.Config) int {
	retry := 0
	if cfg != nil {
		retry = cfg.RequestRetry
	}
	if auth != nil {
		if override, ok := auth.RequestRetryOverride(); ok {
			retry = override
		}
	}
	if retry < 0 {
		retry = 0
	}
	attempts := retry + 1
	if attempts < 1 {
		return 1
	}
	return attempts
}

func antigravityShouldRetryNoCapacity(statusCode int, body []byte) bool {
	if statusCode != http.StatusServiceUnavailable {
		return false
	}
	if len(body) == 0 {
		return false
	}
	msg := strings.ToLower(string(body))
	return strings.Contains(msg, "no capacity available")
}

func antigravityNoCapacityRetryDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := time.Duration(attempt+1) * 250 * time.Millisecond
	if delay > 2*time.Second {
		delay = 2 * time.Second
	}
	return delay
}

func antigravityWait(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func antigravityBaseURLFallbackOrder(auth *modelgateauth.Auth) []string {
	if base := resolveCustomAntigravityBaseURL(auth); base != "" {
		return []string{base}
	}
	return []string{
		antigravitySandboxBaseURLDaily,
		antigravityBaseURLDaily,
		antigravityBaseURLProd,
	}
}

func resolveCustomAntigravityBaseURL(auth *modelgateauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["base_url"]); v != "" {
			return strings.TrimSuffix(v, "/")
		}
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["base_url"].(string); ok {
			v = strings.TrimSpace(v)
			if v != "" {
				return strings.TrimSuffix(v, "/")
			}
		}
	}
	return ""
}

func geminiToAntigravity(modelName string, payload []byte, projectID string) []byte {
	template, _ := sjson.Set(string(payload), "model", modelName)
	template, _ = sjson.Set(template, "userAgent", "antigravity")
	template, _ = sjson.Set(template, "requestType", "agent")

	// Use real project ID from auth if available, otherwise generate random (legacy fallback)
	if projectID != "" {
		template, _ = sjson.Set(template, "project", projectID)
	} else {
		template, _ = sjson.Set(template, "project", generateProjectID())
	}
	template, _ = sjson.Set(template, "requestId", generateRequestID())
	template, _ = sjson.Set(template, "request.sessionId", generateStableSessionID(payload))

	template, _ = sjson.Delete(template, "request.safetySettings")
	template, _ = sjson.Set(template, "request.toolConfig.functionCallingConfig.mode", "VALIDATED")

	if !strings.HasPrefix(modelName, "gemini-3-") && !strings.HasPrefix(modelName, "gemini-3.1-") {
		if thinkingLevel := gjson.Get(template, "request.generationConfig.thinkingConfig.thinkingLevel"); thinkingLevel.Exists() {
			template, _ = sjson.Delete(template, "request.generationConfig.thinkingConfig.thinkingLevel")
			template, _ = sjson.Set(template, "request.generationConfig.thinkingConfig.thinkingBudget", -1)
		}
	}

	if strings.Contains(modelName, "claude") {
		gjson.Get(template, "request.tools").ForEach(func(key, tool gjson.Result) bool {
			tool.Get("functionDeclarations").ForEach(func(funKey, funcDecl gjson.Result) bool {
				if funcDecl.Get("parametersJsonSchema").Exists() {
					template, _ = sjson.SetRaw(template, fmt.Sprintf("request.tools.%d.functionDeclarations.%d.parameters", key.Int(), funKey.Int()), funcDecl.Get("parametersJsonSchema").Raw)
					template, _ = sjson.Delete(template, fmt.Sprintf("request.tools.%d.functionDeclarations.%d.parameters.$schema", key.Int(), funKey.Int()))
					template, _ = sjson.Delete(template, fmt.Sprintf("request.tools.%d.functionDeclarations.%d.parametersJsonSchema", key.Int(), funKey.Int()))
				}
				return true
			})
			return true
		})
	} else {
		template, _ = sjson.Delete(template, "request.generationConfig.maxOutputTokens")
	}

	return []byte(template)
}

func generateRequestID() string {
	return "agent-" + uuid.NewString()
}

func generateSessionID() string {
	randSourceMutex.Lock()
	n := randSource.Int63n(9_000_000_000_000_000_000)
	randSourceMutex.Unlock()
	return "-" + strconv.FormatInt(n, 10)
}

func generateStableSessionID(payload []byte) string {
	contents := gjson.GetBytes(payload, "request.contents")
	if contents.IsArray() {
		for _, content := range contents.Array() {
			if content.Get("role").String() == "user" {
				text := content.Get("parts.0.text").String()
				if text != "" {
					h := sha256.Sum256([]byte(text))
					n := int64(binary.BigEndian.Uint64(h[:8])) & 0x7FFFFFFFFFFFFFFF
					return "-" + strconv.FormatInt(n, 10)
				}
			}
		}
	}
	return generateSessionID()
}

func generateProjectID() string {
	adjectives := []string{"useful", "bright", "swift", "calm", "bold"}
	nouns := []string{"fuze", "wave", "spark", "flow", "core"}
	randSourceMutex.Lock()
	adj := adjectives[randSource.Intn(len(adjectives))]
	noun := nouns[randSource.Intn(len(nouns))]
	randSourceMutex.Unlock()
	randomPart := strings.ToLower(uuid.NewString())[:5]
	return adj + "-" + noun + "-" + randomPart
}

func modelName2Alias(modelName string) string {
	switch modelName {
	case "rev19-uic3-1p":
		return "gemini-2.5-computer-use-preview-10-2025"
	case "gemini-3-pro-high":
		return "gemini-3-pro-preview"
	case "gemini-3.1-pro-high":
		return "gemini-3.1-pro-preview"
	case "gemini-3-flash":
		return "gemini-3-flash-preview"
	case "claude-sonnet-4-5":
		return "gemini-claude-sonnet-4-5"
	case "claude-sonnet-4-5-thinking":
		return "gemini-claude-sonnet-4-5-thinking"
	case "claude-opus-4-5-thinking":
		return "gemini-claude-opus-4-5-thinking"
	case "claude-opus-4-6-thinking":
		return "gemini-claude-opus-4-6-thinking"
	case "claude-sonnet-4-6":
		return "gemini-claude-sonnet-4-6"
	case "chat_20706", "chat_23310", "gemini-2.5-flash-thinking", "gemini-3-pro-low", "gemini-2.5-pro":
		return ""
	default:
		return modelName
	}
}

func alias2ModelName(modelName string) string {
	switch modelName {
	case "gemini-2.5-computer-use-preview-10-2025":
		return "rev19-uic3-1p"
	case "gemini-3-pro-preview":
		return "gemini-3-pro-high"
	case "gemini-3.1-pro-preview":
		return "gemini-3.1-pro-high"
	case "gemini-3-flash-preview":
		return "gemini-3-flash"
	case "gemini-claude-sonnet-4-5":
		return "claude-sonnet-4-5"
	case "gemini-claude-sonnet-4-5-thinking":
		return "claude-sonnet-4-5-thinking"
	case "gemini-claude-opus-4-5-thinking":
		return "claude-opus-4-5-thinking"
	case "gemini-claude-opus-4-6-thinking":
		return "claude-opus-4-6-thinking"
	case "gemini-claude-sonnet-4-6":
		return "claude-sonnet-4-6"
	default:
		return modelName
	}
}

// normalizeAntigravityThinking clamps or removes thinking config based on model support.
// For Claude models, it additionally ensures thinking budget < max_tokens.
func normalizeAntigravityThinking(model string, payload []byte, isClaude bool) []byte {
	payload = util.StripThinkingConfigIfUnsupported(model, payload)
	if !util.ModelSupportsThinking(model) {
		return payload
	}
	budget := gjson.GetBytes(payload, "request.generationConfig.thinkingConfig.thinkingBudget")
	if !budget.Exists() {
		return payload
	}
	raw := int(budget.Int())
	normalized := util.NormalizeThinkingBudget(model, raw)

	if isClaude {
		effectiveMax, setDefaultMax := antigravityEffectiveMaxTokens(model, payload)
		if effectiveMax > 0 && normalized >= effectiveMax {
			normalized = effectiveMax - 1
		}
		minBudget := antigravityMinThinkingBudget(model)
		if minBudget > 0 && normalized >= 0 && normalized < minBudget {
			// Budget is below minimum, remove thinking config entirely
			payload, _ = sjson.DeleteBytes(payload, "request.generationConfig.thinkingConfig")
			return payload
		}
		if setDefaultMax {
			if res, errSet := sjson.SetBytes(payload, "request.generationConfig.maxOutputTokens", effectiveMax); errSet == nil {
				payload = res
			}
		}
	}

	updated, err := sjson.SetBytes(payload, "request.generationConfig.thinkingConfig.thinkingBudget", normalized)
	if err != nil {
		return payload
	}
	return updated
}

// antigravityEffectiveMaxTokens returns the max tokens to cap thinking:
// prefer request-provided maxOutputTokens; otherwise fall back to model default.
// The boolean indicates whether the value came from the model default (and thus should be written back).
func antigravityEffectiveMaxTokens(model string, payload []byte) (max int, fromModel bool) {
	if maxTok := gjson.GetBytes(payload, "request.generationConfig.maxOutputTokens"); maxTok.Exists() && maxTok.Int() > 0 {
		return int(maxTok.Int()), false
	}
	if modelInfo := registry.GetGlobalRegistry().GetModelInfo(model, ""); modelInfo != nil && modelInfo.MaxCompletionTokens > 0 {
		return modelInfo.MaxCompletionTokens, true
	}
	return 0, false
}

// antigravityMinThinkingBudget returns the minimum thinking budget for a model.
// Falls back to -1 if no model info is found.
func antigravityMinThinkingBudget(model string) int {
	if modelInfo := registry.GetGlobalRegistry().GetModelInfo(model, ""); modelInfo != nil && modelInfo.Thinking != nil {
		return modelInfo.Thinking.Min
	}
	return -1
}

// isBare429 checks if a 429 response is a transient rate limit (bare 429) vs real quota exhaustion.
// Bare 429s have no retry info and should be retried internally.
// Real 429s with retry info indicate quota exhaustion and should be propagated.
func isBare429(statusCode int, body []byte) bool {
	if statusCode != http.StatusTooManyRequests {
		return false
	}
	// Check if there's retry info in the response
	retryDelay, err := parseRetryDelay(body)
	return err != nil || retryDelay == nil
}

// attemptJSONRepair tries to fix common JSON syntax errors in malformed function call arguments.
// Returns the fixed JSON string and true if successful, or empty string and false if repair failed.
// Handles: unquoted keys, single quotes, trailing commas.
func attemptJSONRepair(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}

	// First, try to parse as-is
	if json.Valid([]byte(raw)) {
		return raw, true
	}

	fixed := raw

	// Fix 1: Replace single quotes with double quotes (but be careful about escaped quotes)
	// This is a simple approach that handles most cases
	if strings.Contains(fixed, "'") {
		// Simple replacement - may not handle all edge cases but covers common scenarios
		inString := false
		var result strings.Builder
		for i := 0; i < len(fixed); i++ {
			ch := fixed[i]
			if ch == '"' && (i == 0 || fixed[i-1] != '\\') {
				inString = !inString
				result.WriteByte(ch)
			} else if ch == '\'' && !inString {
				result.WriteByte('"')
			} else {
				result.WriteByte(ch)
			}
		}
		fixed = result.String()
	}

	// Fix 2: Add quotes around unquoted keys
	// Pattern: {key: or ,key: where key is alphanumeric/underscore
	// This is a simplified regex-like approach
	fixed = fixUnquotedKeys(fixed)

	// Fix 3: Remove trailing commas before } or ]
	fixed = removeTrailingCommas(fixed)

	// Check if fixed JSON is now valid
	if json.Valid([]byte(fixed)) {
		return fixed, true
	}

	return "", false
}

// fixUnquotedKeys adds double quotes around unquoted JSON keys.
func fixUnquotedKeys(s string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		ch := s[i]

		// Skip strings
		if ch == '"' {
			result.WriteByte(ch)
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					result.WriteByte(s[i])
					i++
				}
				if i < len(s) {
					result.WriteByte(s[i])
					i++
				}
			}
			if i < len(s) {
				result.WriteByte(s[i])
				i++
			}
			continue
		}

		// Look for unquoted keys after { or ,
		if ch == '{' || ch == ',' {
			result.WriteByte(ch)
			i++

			// Skip whitespace
			for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
				result.WriteByte(s[i])
				i++
			}

			// Check if next is an unquoted identifier followed by :
			if i < len(s) && isIdentifierStart(s[i]) {
				keyStart := i
				for i < len(s) && isIdentifierChar(s[i]) {
					i++
				}
				key := s[keyStart:i]

				// Skip whitespace before :
				wsStart := i
				for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
					i++
				}
				ws := s[wsStart:i]

				if i < len(s) && s[i] == ':' {
					// This is an unquoted key - add quotes
					result.WriteByte('"')
					result.WriteString(key)
					result.WriteByte('"')
					result.WriteString(ws)
				} else {
					// Not a key, write as-is
					result.WriteString(key)
					result.WriteString(ws)
				}
			}
			continue
		}

		result.WriteByte(ch)
		i++
	}
	return result.String()
}

// removeTrailingCommas removes trailing commas before } or ].
func removeTrailingCommas(s string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		ch := s[i]

		// Skip strings
		if ch == '"' {
			result.WriteByte(ch)
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					result.WriteByte(s[i])
					i++
				}
				if i < len(s) {
					result.WriteByte(s[i])
					i++
				}
			}
			if i < len(s) {
				result.WriteByte(s[i])
				i++
			}
			continue
		}

		// Look for trailing commas
		if ch == ',' {
			// Look ahead to see if next non-whitespace is } or ]
			j := i + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '\r') {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				// Skip this comma (trailing comma)
				i++
				continue
			}
		}

		result.WriteByte(ch)
		i++
	}
	return result.String()
}

func isIdentifierStart(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_' || ch == '$'
}

func isIdentifierChar(ch byte) bool {
	return isIdentifierStart(ch) || (ch >= '0' && ch <= '9')
}

// checkForMalformedFunctionCall checks if a streaming response contains a MALFORMED_FUNCTION_CALL error.
// Returns the error message if found, empty string otherwise.
func checkForMalformedFunctionCall(chunk []byte) string {
	// Check for finishReason: "MALFORMED_FUNCTION_CALL"
	finishReason := gjson.GetBytes(chunk, "candidates.0.finishReason").String()
	if finishReason == "MALFORMED_FUNCTION_CALL" {
		// Extract the error message from finishMessage
		finishMessage := gjson.GetBytes(chunk, "candidates.0.finishMessage").String()
		if finishMessage != "" {
			return finishMessage
		}
		return "MALFORMED_FUNCTION_CALL detected"
	}
	return ""
}
