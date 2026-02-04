package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	copilotauth "github.com/shariqriazz/modelgate/internal/auth/copilot"
	"github.com/shariqriazz/modelgate/internal/config"
	"github.com/shariqriazz/modelgate/internal/registry"
	"github.com/shariqriazz/modelgate/internal/util"
	modelgateauth "github.com/shariqriazz/modelgate/sdk/cliproxy/auth"
	modelgateexecutor "github.com/shariqriazz/modelgate/sdk/cliproxy/executor"
	sdktranslator "github.com/shariqriazz/modelgate/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	githubCopilotBaseURL       = "https://api.githubcopilot.com"
	githubCopilotChatPath      = "/chat/completions"
	githubCopilotResponsesPath = "/responses"
	githubCopilotMessagesPath  = "/v1/messages"
	githubCopilotAuthType      = "github-copilot"
	githubCopilotTokenCacheTTL = 25 * time.Minute
	// tokenExpiryBuffer is the time before expiry when we should refresh the token.
	tokenExpiryBuffer = 5 * time.Minute
	// maxScannerBufferSize is the maximum buffer size for SSE scanning (20MB).
	maxScannerBufferSize = 20_971_520

	// Copilot API header values.
	copilotUserAgent     = "GithubCopilot/1.0"
	copilotEditorVersion = "vscode/1.109.0-20260124"
	copilotPluginVersion = "copilot-chat/0.37.2026013101"
	copilotIntegrationID = "vscode-chat"
	copilotOpenAIIntent  = "conversation-panel"
	copilotAPIVersion    = "2025-10-01"
	copilotThinkingBeta  = "interleaved-thinking-2025-05-14,context-management-2025-06-27"
)

// GitHubCopilotExecutor handles requests to the GitHub Copilot API.
type GitHubCopilotExecutor struct {
	cfg   *config.Config
	mu    sync.RWMutex
	cache map[string]*cachedAPIToken
}

// cachedAPIToken stores a cached Copilot API token with its expiry.
type cachedAPIToken struct {
	token     string
	expiresAt time.Time
}

// NewGitHubCopilotExecutor constructs a new executor instance.
func NewGitHubCopilotExecutor(cfg *config.Config) *GitHubCopilotExecutor {
	return &GitHubCopilotExecutor{
		cfg:   cfg,
		cache: make(map[string]*cachedAPIToken),
	}
}

// Identifier implements ProviderExecutor.
func (e *GitHubCopilotExecutor) Identifier() string { return githubCopilotAuthType }

// PrepareRequest implements ProviderExecutor.
func (e *GitHubCopilotExecutor) PrepareRequest(req *http.Request, auth *modelgateauth.Auth) error {
	if req == nil {
		return nil
	}
	ctx := req.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	apiToken, errToken := e.ensureAPIToken(ctx, auth)
	if errToken != nil {
		return errToken
	}
	e.applyHeaders(req, apiToken, sdktranslator.FromString("openai"))
	return nil
}

// HttpRequest injects GitHub Copilot credentials into the request and executes it.
func (e *GitHubCopilotExecutor) HttpRequest(ctx context.Context, auth *modelgateauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("github-copilot executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if errPrepare := e.PrepareRequest(httpReq, auth); errPrepare != nil {
		return nil, errPrepare
	}
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute handles non-streaming requests to GitHub Copilot.
func (e *GitHubCopilotExecutor) Execute(ctx context.Context, auth *modelgateauth.Auth, req modelgateexecutor.Request, opts modelgateexecutor.Options) (resp modelgateexecutor.Response, err error) {
	apiToken, errToken := e.ensureAPIToken(ctx, auth)
	if errToken != nil {
		return resp, errToken
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	// Use "codex" translator for GPT-5 models (responses API), "openai" for others (chat completions)
	toFormat := "openai"
	if isCopilotClaudeModel(req.Model) {
		toFormat = "claude"
	} else if isGPT5Model(req.Model) {
		toFormat = "codex"
	}
	to := sdktranslator.FromString(toFormat)
	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, req.Model, originalPayload, false)
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)
	body = e.normalizeModel(req.Model, body)
	requestedModel := payloadRequestedModel(opts, req.Model)
	body = applyPayloadConfigWithRoot(e.cfg, req.Model, to.String(), "", body, originalTranslated, requestedModel)
	if isCopilotClaudeFormat(to) {
		body = normalizeCopilotClaudeThinking(req.Model, body)
	}
	body, _ = sjson.SetBytes(body, "stream", false)
	body, _ = sjson.DeleteBytes(body, "stream_options")

	url := githubCopilotBaseURL + getEndpointPath(req.Model, to)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	e.applyHeaders(httpReq, apiToken, to)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("github-copilot executor: close response body error: %v", errClose)
		}
	}()

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	if !isHTTPSuccess(httpResp.StatusCode) {
		data, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, data)
		log.Debugf("github-copilot executor: upstream error status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = statusErr{code: httpResp.StatusCode, msg: string(data)}
		return resp, err
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, data)

	detail := parseOpenAIUsage(data)
	if detail.TotalTokens == 0 && isCopilotClaudeFormat(to) {
		detail = parseClaudeUsage(data)
	}
	if detail.TotalTokens > 0 {
		reporter.publish(ctx, detail)
	}

	var param any
	converted := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, data, &param)
	resp = modelgateexecutor.Response{Payload: []byte(converted)}
	reporter.ensurePublished(ctx)
	return resp, nil
}

// ExecuteStream handles streaming requests to GitHub Copilot.
func (e *GitHubCopilotExecutor) ExecuteStream(ctx context.Context, auth *modelgateauth.Auth, req modelgateexecutor.Request, opts modelgateexecutor.Options) (stream <-chan modelgateexecutor.StreamChunk, err error) {
	apiToken, errToken := e.ensureAPIToken(ctx, auth)
	if errToken != nil {
		return nil, errToken
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	// Use "codex" translator for GPT-5 models (responses API), "openai" for others (chat completions)
	toFormat := "openai"
	if isCopilotClaudeModel(req.Model) {
		toFormat = "claude"
	} else if isGPT5Model(req.Model) {
		toFormat = "codex"
	}
	to := sdktranslator.FromString(toFormat)
	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, req.Model, originalPayload, false)
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)
	body = e.normalizeModel(req.Model, body)
	requestedModel := payloadRequestedModel(opts, req.Model)
	body = applyPayloadConfigWithRoot(e.cfg, req.Model, to.String(), "", body, originalTranslated, requestedModel)
	if isCopilotClaudeFormat(to) {
		body = normalizeCopilotClaudeThinking(req.Model, body)
	}
	body, _ = sjson.SetBytes(body, "stream", true)
	// Copilot Claude Messages API does not support stream_options.
	if !isCopilotClaudeFormat(to) {
		// Enable stream options for usage stats in stream
		body, _ = sjson.SetBytes(body, "stream_options.include_usage", true)
	} else {
		body, _ = sjson.DeleteBytes(body, "stream_options")
	}

	url := githubCopilotBaseURL + getEndpointPath(req.Model, to)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	e.applyHeaders(httpReq, apiToken, to)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	if !isHTTPSuccess(httpResp.StatusCode) {
		data, readErr := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("github-copilot executor: close response body error: %v", errClose)
		}
		if readErr != nil {
			recordAPIResponseError(ctx, e.cfg, readErr)
			return nil, readErr
		}
		appendAPIResponseChunk(ctx, e.cfg, data)
		log.Debugf("github-copilot executor: upstream error status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = statusErr{code: httpResp.StatusCode, msg: string(data)}
		return nil, err
	}

	out := make(chan modelgateexecutor.StreamChunk)
	stream = out

	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("github-copilot executor: close response body error: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, maxScannerBufferSize)
		var param any

		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)

			// Parse SSE data
			if bytes.HasPrefix(line, dataTag) {
				data := bytes.TrimSpace(line[5:])
				if bytes.Equal(data, []byte("[DONE]")) {
					continue
				}
				if detail, ok := parseOpenAIStreamUsage(line); ok {
					if detail.TotalTokens == 0 && isCopilotClaudeFormat(to) {
						if claudeDetail, ok := parseClaudeStreamUsage(line); ok {
							reporter.publish(ctx, claudeDetail)
						}
					} else {
						reporter.publish(ctx, detail)
					}
				} else if isCopilotClaudeFormat(to) {
					if detail, ok := parseClaudeStreamUsage(line); ok {
						reporter.publish(ctx, detail)
					}
				}
			}

			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, bytes.Clone(line), &param)
			for i := range chunks {
				out <- modelgateexecutor.StreamChunk{Payload: []byte(chunks[i])}
			}
		}

		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			out <- modelgateexecutor.StreamChunk{Err: errScan}
		} else {
			reporter.ensurePublished(ctx)
		}
	}()

	return stream, nil
}

// CountTokens is not supported for GitHub Copilot.
func (e *GitHubCopilotExecutor) CountTokens(_ context.Context, _ *modelgateauth.Auth, _ modelgateexecutor.Request, _ modelgateexecutor.Options) (modelgateexecutor.Response, error) {
	return modelgateexecutor.Response{}, statusErr{code: http.StatusNotImplemented, msg: "count tokens not supported for github-copilot"}
}

// Refresh validates the GitHub token is still working.
// GitHub OAuth tokens don't expire traditionally, so we just validate.
func (e *GitHubCopilotExecutor) Refresh(ctx context.Context, auth *modelgateauth.Auth) (*modelgateauth.Auth, error) {
	if auth == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing auth"}
	}

	// Get the GitHub access token
	accessToken := metaStringValue(auth.Metadata, "access_token")
	if accessToken == "" {
		return auth, nil
	}

	// Validate the token can still get a Copilot API token
	copilotAuth := copilotauth.NewCopilotAuth(e.cfg)
	_, err := copilotAuth.GetCopilotAPIToken(ctx, accessToken)
	if err != nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: fmt.Sprintf("github-copilot token validation failed: %v", err)}
	}

	return auth, nil
}

// ensureAPIToken gets or refreshes the Copilot API token.
func (e *GitHubCopilotExecutor) ensureAPIToken(ctx context.Context, auth *modelgateauth.Auth) (string, error) {
	if auth == nil {
		return "", statusErr{code: http.StatusUnauthorized, msg: "missing auth"}
	}

	// Get the GitHub access token
	accessToken := metaStringValue(auth.Metadata, "access_token")
	if accessToken == "" {
		return "", statusErr{code: http.StatusUnauthorized, msg: "missing github access token"}
	}

	// Check for cached API token using thread-safe access
	e.mu.RLock()
	if cached, ok := e.cache[accessToken]; ok && cached.expiresAt.After(time.Now().Add(tokenExpiryBuffer)) {
		e.mu.RUnlock()
		return cached.token, nil
	}
	e.mu.RUnlock()

	// Get a new Copilot API token
	copilotAuth := copilotauth.NewCopilotAuth(e.cfg)
	apiToken, err := copilotAuth.GetCopilotAPIToken(ctx, accessToken)
	if err != nil {
		return "", statusErr{code: http.StatusUnauthorized, msg: fmt.Sprintf("failed to get copilot api token: %v", err)}
	}

	// Cache the token with thread-safe access
	expiresAt := time.Now().Add(githubCopilotTokenCacheTTL)
	if apiToken.ExpiresAt > 0 {
		expiresAt = time.Unix(apiToken.ExpiresAt, 0)
	}
	e.mu.Lock()
	e.cache[accessToken] = &cachedAPIToken{
		token:     apiToken.Token,
		expiresAt: expiresAt,
	}
	e.mu.Unlock()

	return apiToken.Token, nil
}

// applyHeaders sets the required headers for GitHub Copilot API requests.
func (e *GitHubCopilotExecutor) applyHeaders(r *http.Request, apiToken string, format sdktranslator.Format) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+apiToken)
	r.Header.Set("Accept", "application/json")
	r.Header.Set("User-Agent", copilotUserAgent)
	r.Header.Set("Editor-Version", copilotEditorVersion)
	r.Header.Set("Editor-Plugin-Version", copilotPluginVersion)
	r.Header.Set("Openai-Intent", copilotOpenAIIntent)
	r.Header.Set("Copilot-Integration-Id", copilotIntegrationID)
	r.Header.Set("X-GitHub-Api-Version", copilotAPIVersion)
	r.Header.Set("X-Request-Id", uuid.NewString())
	r.Header.Set("X-Initiator", "agent")
	r.Header.Set("VScode-SessionId", uuid.NewString())
	r.Header.Set("VScode-MachineId", uuid.NewString())
	if isCopilotClaudeFormat(format) {
		r.Header.Set("anthropic-beta", copilotThinkingBeta)
	}
}

// normalizeModel strips the "copilot-" prefix from model names before sending to GitHub Copilot API.
// This allows us to use prefixed model IDs internally (e.g., "copilot-gpt-5.2") to avoid conflicts
// with other providers while still sending the correct model name to GitHub Copilot.
func (e *GitHubCopilotExecutor) normalizeModel(model string, body []byte) []byte {
	normalized := strings.TrimPrefix(model, "copilot-")
	if normalized != model {
		body, _ = sjson.SetBytes(body, "model", normalized)
	}
	return body
}

// isGPT5Model checks if the model is a GPT-5 series model that requires the /responses endpoint.
func isGPT5Model(model string) bool {
	normalized := strings.TrimPrefix(model, "copilot-")
	return strings.HasPrefix(normalized, "gpt-5")
}

func isCopilotClaudeModel(model string) bool {
	normalized := strings.TrimPrefix(model, "copilot-")
	return strings.HasPrefix(normalized, "claude-")
}

// getEndpointPath returns the appropriate endpoint path based on the model.
// GPT-5 series models use /responses, others use /chat/completions.
func getEndpointPath(model string, format sdktranslator.Format) string {
	if isCopilotClaudeFormat(format) {
		return githubCopilotMessagesPath
	}
	if isGPT5Model(model) {
		return githubCopilotResponsesPath
	}
	return githubCopilotChatPath
}

func isCopilotClaudeFormat(format sdktranslator.Format) bool {
	return format == sdktranslator.FormatClaude
}

func normalizeCopilotClaudeThinking(model string, body []byte) []byte {
	if !util.ModelSupportsThinking(model) {
		return body
	}
	maxTokens := gjson.GetBytes(body, "max_tokens")
	if !maxTokens.Exists() {
		if info := registry.GetGlobalRegistry().GetModelInfo(model, ""); info != nil && info.MaxCompletionTokens > 0 {
			updated, err := sjson.SetBytes(body, "max_tokens", info.MaxCompletionTokens)
			if err == nil {
				body = updated
			}
			maxTokens = gjson.GetBytes(body, "max_tokens")
		}
	}
	maxVal := int(maxTokens.Int())
	if maxVal <= 0 {
		return body
	}
	thinking := gjson.GetBytes(body, "thinking")
	if !thinking.Exists() {
		return body
	}
	thinkingType := strings.ToLower(strings.TrimSpace(thinking.Get("type").String()))
	if thinkingType == "disabled" {
		return body
	}
	budgetVal := int(thinking.Get("budget_tokens").Int())
	if budgetVal <= 0 {
		budgetVal = util.NormalizeThinkingBudget(model, -1)
	}
	normalized := util.NormalizeThinkingBudget(model, budgetVal)
	if normalized >= maxVal {
		normalized = maxVal - 1
	}
	if minBudget := minThinkingBudget(model); minBudget > 0 && normalized > 0 && normalized < minBudget {
		if updated, err := sjson.DeleteBytes(body, "thinking"); err == nil {
			return updated
		}
		return body
	}
	updated, err := sjson.SetBytes(body, "thinking.type", "enabled")
	if err == nil {
		body = updated
	}
	updated, err = sjson.SetBytes(body, "thinking.budget_tokens", normalized)
	if err == nil {
		body = updated
	}
	return body
}

func minThinkingBudget(model string) int {
	if info := registry.GetGlobalRegistry().GetModelInfo(model, ""); info != nil && info.Thinking != nil {
		return info.Thinking.Min
	}
	return 0
}

// isHTTPSuccess checks if the status code indicates success (2xx).
func isHTTPSuccess(statusCode int) bool {
	return statusCode >= 200 && statusCode < 300
}
