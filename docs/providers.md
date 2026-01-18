# Provider Executors

Provider executors are the core abstraction for communicating with upstream AI service APIs. Each executor handles authentication, request translation, and response processing for a specific provider.

## Role of Provider Executors

A provider executor:
- Manages authentication credentials (OAuth tokens, API keys)
- Translates requests from the internal format to provider-specific formats
- Handles both streaming and non-streaming API calls
- Reports usage metrics for tracking and billing
- Applies provider-specific request transformations

## Supported Providers

| Provider | Identifier | Auth Method | Upstream API |
|----------|------------|-------------|--------------|
| Antigravity | `antigravity` | OAuth2 (Google) | `cloudcode-pa.googleapis.com` |
| Codex | `codex` | API Key | `chatgpt.com/backend-api/codex` |
| GitHub Copilot | `github-copilot` | OAuth Token | `api.githubcopilot.com` |
| Gemini CLI | `gemini-cli` | OAuth2 (Google) | `cloudcode-pa.googleapis.com` |
| iFlow | `iflow` | API Key (from OAuth) | Configurable base URL |
| Qwen | `qwen` | Bearer Token | `portal.qwen.ai/v1` |

## Common Executor Interface

All executors implement these core methods:

### `Identifier() string`
Returns the provider's unique identifier string used for routing and configuration.
- Reference: `antigravity_executor.go:82`, `codex_executor.go:37`, `github_copilot_executor.go:63`

### `PrepareRequest(req *http.Request, auth *Auth) error`
Injects provider credentials and headers into an outgoing HTTP request.
- Reference: `antigravity_executor.go:85-95`, `codex_executor.go:40-54`

### `HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error)`
Prepares and executes an HTTP request with proper authentication.
- Reference: `antigravity_executor.go:98-111`, `github_copilot_executor.go:79-92`

### `Execute(ctx context.Context, auth *Auth, req Request, opts Options) (Response, error)`
Performs a non-streaming request to the provider API.
- Reference: `antigravity_executor.go:114`, `codex_executor.go:68`

### `ExecuteStream(ctx context.Context, auth *Auth, req Request, opts Options) (<-chan StreamChunk, error)`
Performs a streaming request, returning a channel of response chunks.

## Provider-Specific Details

### Antigravity
- Uses Google OAuth2 with automatic token refresh (`antigravity_executor.go:49`)
- Supports both Claude and Gemini models with different handling (`antigravity_executor.go:115-117`)
- Implements retry logic for empty responses and malformed function calls (`antigravity_executor.go:55-61`)
- Applies thinking metadata and normalization for reasoning models

### Codex
- Falls back to legacy ClientAdapter if API key unavailable (`codex_executor.go:30-32`)
- Uses OpenAI Responses API format (`codex_executor.go:30`)
- Applies reasoning effort metadata for supported models (`codex_executor.go:91`)
- Injects custom user agent for request identification

### GitHub Copilot
- Caches API tokens with TTL of 25 minutes (`github_copilot_executor.go:29`)
- Applies Copilot-specific headers: User-Agent, Editor-Version, Plugin-Version (`github_copilot_executor.go:35-39`)
- Supports both `/chat/completions` and `/responses` endpoints (`github_copilot_executor.go:25-26`)
- Uses GPT-5 translator for specific models via responses endpoint

### Gemini CLI
- Uses Google Cloud Code Assist endpoints (`gemini_cli_executor.go:35`)
- OAuth2 scopes: cloud-platform, userinfo.email, userinfo.profile (`gemini_cli_executor.go:43-47`)
- Large stream scanner buffer (50MB) for handling large responses (`gemini_cli_executor.go:41`)

### iFlow
- OpenAI-compatible chat completions format (`iflow_executor.go:24`)
- Derives API keys from OAuth credentials (`iflow_executor.go:68-69`)
- Applies custom thinking configuration (`iflow_executor.go:92`)
- Preserves reasoning content in messages for chain-of-thought

### Qwen
- Uses OpenAI-compatible format at `portal.qwen.ai` (`qwen_executor.go:69`)
- Applies custom headers mimicking Google API Node.js client (`qwen_executor.go:24-26`)
- Supports reasoning effort metadata for reasoning models (`qwen_executor.go:82`)

## Adding a New Provider

1. **Create executor file**: `internal/runtime/executor/<provider>_executor.go`

2. **Implement required methods**:
   - `Identifier()` - return unique provider identifier
   - `PrepareRequest()` - inject auth credentials into requests
   - `HttpRequest()` - execute authenticated HTTP requests
   - `Execute()` - handle non-streaming requests
   - `ExecuteStream()` - handle streaming requests

3. **Add authentication handler**: Create auth package in `internal/auth/<provider>/` if custom OAuth or token handling is needed

4. **Register translator**: Add request/response format translator in `sdk/translator/` if the provider uses a non-OpenAI format

5. **Update configuration**: Add provider-specific config fields to `internal/config/`

6. **Register executor**: Add executor instantiation in the runtime initialization code

## Request/Response Types

Defined in `sdk/cliproxy/executor/types.go`:

- **Request**: Model, Payload, Format, Metadata
- **Options**: Stream, Headers, Query, OriginalRequest, SourceFormat
- **Response**: Payload, Metadata
- **StreamChunk**: Payload, Err
- **StatusError**: Interface for HTTP status code errors
