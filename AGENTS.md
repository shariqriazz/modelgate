# AGENTS.md

## ModelGate - AI/LLM API Gateway Proxy

A high-performance API gateway that provides unified OpenAI/Gemini/Claude-compatible interfaces for multiple AI providers (Antigravity, Codex, Qwen, IFlow, GitHub Copilot). Handles OAuth credential management, load balancing, request/response translation, and streaming.

## Commands

```bash
# Build
go build -o modelgate ./cmd/server

# Run tests
go test ./...

# Run specific test
go test ./internal/api/... -v

# Docker build
./docker-build.sh
# or
docker build -t modelgate .

# Start server
./modelgate -config config.yaml

# OAuth login (provider-specific flags)
./modelgate -antigravity-login
./modelgate -codex-login
./modelgate -github-copilot-login
```

## Architecture

Go 1.24 | Gin HTTP | tidwall/gjson | OAuth 2.0 | YAML config | Docker

### Key Directories

- `cmd/server/` - Main entry point, CLI flags, initialization
- `internal/runtime/executor/` - Provider-specific executors (Antigravity, Codex, GitHub Copilot, etc.)
- `internal/translator/` - Request/response format translation between API schemas
- `internal/watcher/` - Hot-reload config/auth file watching
- `internal/api/` - HTTP server, handlers, middleware
- `sdk/` - Reusable library for embedding proxy functionality
- `sdk/cliproxy/` - Core proxy service with builder pattern
- `sdk/auth/` - OAuth flow implementations per provider
- `sdk/translator/` - Schema translation pipeline
- `auths/` - Credential storage directory (gitignored)

### Provider Flow

1. Request arrives at unified API endpoint (OpenAI/Gemini/Claude format)
2. Translator converts request to target provider format
3. Executor sends request to upstream provider with auth
4. Response translated back to requested format

## Critical Patterns

- **Executor per provider**: Each provider (antigravity, codex, copilot, etc.) has its own executor in `internal/runtime/executor/`. Executors handle auth injection, streaming, and provider-specific quirks.
- **Translator registry**: Translations registered via `sdk/translator`. Format pairs like `openai->claude`, `gemini->openai` are bidirectional. Check `internal/translator/init.go` for registered conversions.
- **Auth refresh**: OAuth tokens auto-refresh via `sdk/auth/refresh_registry.go`. Each provider implements `RefreshFunc`.
- **Hot reload**: Config and auth file changes trigger live reload via `internal/watcher/`. No server restart needed.
- **Streaming uses SSE**: All streaming responses use Server-Sent Events format. Streaming code paths are separate from non-streaming.
- **tidwall/gjson for JSON**: Use `gjson.Get()`/`sjson.Set()` for JSON manipulation, not `encoding/json` struct marshaling for performance-critical paths.

## Modularization

- Executor pattern: `{provider}_executor.go` in `internal/runtime/executor/`
- Translator pattern: `{source}_{target}_request.go` / `{source}_{target}_response.go`
- Use `_helpers.go` suffix for shared utilities within a package

## Documentation

Read relevant docs BEFORE changes. Update docs AFTER changes.

| Area | Doc |
|------|-----|
| Architecture | `docs/architecture.md` |
| Providers | `docs/providers.md` |
| Translators | `docs/translators.md` |
| Authentication | `docs/authentication.md` |
| API Endpoints | `docs/api-endpoints.md` |
| Configuration | `docs/configuration.md` |
| SDK Embedding | `docs/sdk-embedding.md` |
| Hot-Reload | `docs/watcher.md` |

## Maintaining This File

| DO | DON'T |
|----|-------|
| Project-specific info only | Duplicate global rules |
| Point to code, not copy | Duplicate code snippets |
| Update when patterns change | List every edge case |
| Describe capabilities | List file paths |
| Deployment | `docs/deployment.md` |
