# API Endpoints

ModelGate provides API compatibility layers for OpenAI, Gemini, and Claude formats, allowing clients to use their preferred SDK while routing to various backend providers.

## Authentication

All API endpoints (except root and OAuth callbacks) require authentication via:
- `Authorization: Bearer <api-key>` header
- `api-key: <api-key>` header

See `sdk/access/manager.go` for authentication provider implementation.

## API Compatibility Layers

| Format | Base Path | Description |
|--------|-----------|-------------|
| OpenAI | `/v1` | OpenAI-compatible chat completions and responses API |
| Gemini | `/v1beta` | Google Gemini-compatible API |
| Claude | `/v1/messages` | Anthropic Claude-compatible messages API |

## Main API Endpoints

| Path | Method | Description | Format |
|------|--------|-------------|--------|
| `/` | GET | Server info and available endpoints | JSON |
| `/v1/models` | GET | List available models (routes by User-Agent) | OpenAI/Claude |
| `/v1/chat/completions` | POST | Chat completions (streaming/non-streaming) | OpenAI |
| `/v1/completions` | POST | Text completions | OpenAI |
| `/v1/messages` | POST | Claude messages API | Claude |
| `/v1/messages/count_tokens` | POST | Count tokens for Claude request | Claude |
| `/v1/responses` | POST | OpenAI Responses API | OpenAI |
| `/v1beta/models` | GET | List Gemini models | Gemini |
| `/v1beta/models/*action` | POST | Gemini operations (generateContent, etc.) | Gemini |
| `/v1beta/models/*action` | GET | Gemini model info | Gemini |

Route registration: `internal/api/server.go:277-302`

### Request/Response Formats

**OpenAI Chat Completions** (`/v1/chat/completions`):
- Request: Standard OpenAI chat completion format with `model`, `messages`, `stream`
- Response: OpenAI chat completion response or SSE stream
- Handler: `sdk/api/handlers/openai/openai_handlers.go:44`

**Claude Messages** (`/v1/messages`):
- Request: Anthropic messages format with `model`, `messages`, `stream`
- Response: Claude message response or SSE stream
- Handler: `sdk/api/handlers/claude/code_handlers.go:62`

**Gemini** (`/v1beta/models/*action`):
- Request: Gemini API format with `:generateContent` action suffix
- Response: Gemini response or SSE stream (alt=sse)
- Handler: `sdk/api/handlers/gemini/gemini_handlers.go:72`

## Management API

Base path: `/v0/management`

Requires management key via:
- `Authorization: Bearer <management-key>`
- `X-Management-Key: <management-key>`

Set via `MANAGEMENT_PASSWORD` env var or `remote_management.secret_key` in config.

Handler: `internal/api/handlers/management/handler.go:49`

### Management Endpoints

| Path | Method | Description |
|------|--------|-------------|
| `/v0/management/config` | GET | Get current configuration |
| `/v0/management/config.yaml` | GET/PUT | Get/update YAML configuration |
| `/v0/management/usage` | GET | Get usage statistics |
| `/v0/management/usage/export` | GET | Export usage statistics |
| `/v0/management/usage/import` | POST | Import usage statistics |
| `/v0/management/debug` | GET/PUT | Get/set debug mode |
| `/v0/management/api-keys` | GET/PUT/PATCH/DELETE | Manage API keys |
| `/v0/management/gemini-api-key` | GET/PUT/PATCH/DELETE | Manage Gemini API keys |
| `/v0/management/claude-api-key` | GET/PUT/PATCH/DELETE | Manage Claude API keys |
| `/v0/management/codex-api-key` | GET/PUT/PATCH/DELETE | Manage Codex API keys |
| `/v0/management/logs` | GET/DELETE | View/delete server logs |
| `/v0/management/request-log` | GET/PUT | Request logging settings |
| `/v0/management/proxy-url` | GET/PUT/DELETE | Proxy URL configuration |
| `/v0/management/routing/strategy` | GET/PUT | Load balancing strategy |
| `/v0/management/ampcode/*` | * | Amp module configuration |
| `/v0/management/auth-files` | GET/POST/DELETE | Manage auth files |
| `/v0/management/get-auth-status` | GET | OAuth authentication status |

Route registration: `internal/api/server.go:380-500`

## OAuth Callback Endpoints

These endpoints receive OAuth provider redirects:

| Path | Provider |
|------|----------|
| `/anthropic/callback` | Anthropic |
| `/codex/callback` | Codex/OpenAI |
| `/google/callback` | Google/Gemini |
| `/kiro/callback` | Kiro |
| `/antigravity/callback` | Antigravity |
| `/iflow/callback` | IFlow |

## Internal Endpoints

| Path | Method | Description |
|------|--------|-------------|
| `/management.html` | GET | Management control panel |
| `/keep-alive` | GET | Keep-alive heartbeat (when enabled) |
| `/api/event_logging/batch` | POST | Telemetry sink (returns 200 OK) |
| `/v1internal:method` | POST | Gemini CLI internal handler |

## Error Response Format

All endpoints return OpenAI-compatible error responses:

```json
{
  "error": {
    "message": "Error description",
    "type": "invalid_request_error",
    "code": "error_code"
  }
}
```

Error types: `authentication_error`, `permission_error`, `rate_limit_error`, `invalid_request_error`, `server_error`

See `sdk/api/handlers/handlers.go:59-92` for error response building.
