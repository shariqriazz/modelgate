# AGENTS.md

This file provides guidance to AI agents when working with code in this repository.

## ModelGate - AI/LLM API Gateway Proxy

A high-performance API gateway that provides unified OpenAI/Gemini/Claude-compatible interfaces for multiple AI providers (Antigravity, Codex, Qwen, IFlow, GitHub Copilot). Handles OAuth credential management, load balancing, request/response translation, and streaming.

---

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

---

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

---

## Critical Patterns

- **Executor per provider**: Each provider (antigravity, codex, copilot, etc.) has its own executor in `internal/runtime/executor/`. Executors handle auth injection, streaming, and provider-specific quirks.
- **Translator registry**: Translations registered via `sdk/translator`. Format pairs like `openai->claude`, `gemini->openai` are bidirectional. Check `internal/translator/init.go` for registered conversions.
- **Auth refresh**: OAuth tokens auto-refresh via `sdk/auth/refresh_registry.go`. Each provider implements `RefreshFunc`.
- **Hot reload**: Config and auth file changes trigger live reload via `internal/watcher/`. No server restart needed.
- **Streaming uses SSE**: All streaming responses use Server-Sent Events format. Streaming code paths are separate from non-streaming.
- **tidwall/gjson for JSON**: Use `gjson.Get()`/`sjson.Set()` for JSON manipulation, not `encoding/json` struct marshaling for performance-critical paths.

---

## Documentation Cross-References

| Area | Doc |
|------|-----|
| Architecture | [`docs/architecture.md`](docs/architecture.md) |
| Providers | [`docs/providers.md`](docs/providers.md) |
| Translators | [`docs/translators.md`](docs/translators.md) |
| Authentication | [`docs/authentication.md`](docs/authentication.md) |
| API Endpoints | [`docs/api-endpoints.md`](docs/api-endpoints.md) |
| Configuration | [`docs/configuration.md`](docs/configuration.md) |
| SDK Embedding | [`docs/sdk-embedding.md`](docs/sdk-embedding.md) |
| Hot-Reload | [`docs/watcher.md`](docs/watcher.md) |
| Deployment | [`docs/deployment.md`](docs/deployment.md) |

**Read the relevant doc BEFORE making changes. Update the doc AFTER making changes.**

---

## Code Quality

- **Max file size**: 500-600 LOC strict. Split larger files into focused modules.
- **Read before modifying** - understand existing patterns first
- **Modularization applies to ALL code**: frontend, backend, utilities - no exceptions

---

## Modularization Rules (Go)

### General Principles
- **300 LOC threshold**: Start planning to split when approaching 300 LOC
- **500-600 LOC hard limit**: Never exceed - refactor immediately
- **Single responsibility**: Each file/module should do one thing well

### Go-Specific Patterns
- Package per feature: `internal/{feature}/`
- Interface definitions separate from implementations
- Keep packages focused and cohesive
- Use `_helpers.go` suffix for shared utilities within a package
- Executor pattern: `{provider}_executor.go` in `internal/runtime/executor/`
- Translator pattern: `{source}_{target}_request.go` / `{source}_{target}_response.go`

### When to Split
- File exceeds 300 LOC
- Multiple distinct responsibilities
- Reusable logic that could be shared
- Complex logic that deserves isolation

---

## Development Workflow

1. Read this file and relevant code before changes
2. Make focused changes (avoid over-engineering)
3. Run `go test ./...` before commits
4. After code changes: update relevant docs and AGENTS.md if needed
5. **Always ask before commit/push** - permission for one commit does NOT carry over to subsequent commits

---

## Subagent Usage

Use subagents liberally for parallel work. Run independent agents concurrently (different files/concerns). Only sequence agents that modify the same files.

---

## Maintaining This File

**Target: <300 lines.** Loads into every AI session.

| DO | DON'T |
|----|-------|
| Universal info only | Task-specific details |
| Point to code, not copy | Duplicate code snippets |
| Update when patterns change | List every edge case |
