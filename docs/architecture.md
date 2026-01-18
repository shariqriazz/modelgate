# ModelGate Architecture

## Overview

ModelGate is an AI/LLM API gateway that provides unified OpenAI/Claude/Gemini-compatible endpoints for multiple AI providers. It enables clients to use familiar API formats (OpenAI, Claude, Gemini) while routing requests to various backends including Codex, Gemini CLI, Anthropic, GitHub Copilot, and others.

**Core Design Goals:**
- Unified API surface for heterogeneous AI backends
- Hot-reloadable configuration and authentication
- Provider-agnostic request translation
- Quota management with automatic failover

## Request Flow

```
┌─────────┐     ┌──────────────┐     ┌────────────┐     ┌──────────┐     ┌──────────┐
│  Client │────▶│  API Handler │────▶│ Translator │────▶│ Executor │────▶│ Provider │
└─────────┘     └──────────────┘     └────────────┘     └──────────┘     └──────────┘
     │                 │                   │                  │                │
     │  OpenAI/Claude  │   Auth + Route    │  Format Convert  │  HTTP + Creds  │
     │  /Gemini format │   by model        │  (e.g. OpenAI→   │  injection     │
     │                 │                   │   Codex native)  │                │
     └─────────────────┴───────────────────┴──────────────────┴────────────────┘
```

1. **Client** sends request in OpenAI/Claude/Gemini format to `/v1/chat/completions`, `/v1/messages`, etc.
2. **API Handler** (`sdk/api/handlers/`) authenticates via AccessManager, extracts model, routes to appropriate handler
3. **Translator** (`sdk/translator/`, `internal/translator/`) converts between API formats (e.g., OpenAI→Codex native)
4. **Executor** (`internal/runtime/executor/`) injects provider credentials and executes HTTP request
5. **Provider** (Codex, Gemini, Claude API, etc.) returns response, which flows back through translator

## Key Components

### Entry Point & Server Lifecycle
- `cmd/server/main.go:48-69` - CLI flags, config loading, token store selection
- `cmd/server/main.go:308-320` - Service startup via `cmd.StartService()`

### HTTP Server & Routing
- `internal/api/server.go:109-122` - Server struct: holds Gin engine, config, handlers
- `internal/api/server.go:162-194` - NewServer: middleware setup (logging, CORS, auth)
- `internal/api/server.go:237-276` - setupRoutes: defines `/v1/*`, `/v1beta/*` endpoints

OpenAI routes at `/v1/` include chat completions, messages (Claude), and responses. Gemini routes at `/v1beta/` for native Gemini clients.

### Service Layer (SDK)
- `sdk/cliproxy/service.go:27-68` - Service struct: orchestrates server, watchers, auth managers
- `sdk/cliproxy/builder.go:19-48` - Builder pattern for constructing Service with providers

The Builder exists to allow external embedding of ModelGate with custom providers—decoupling configuration from construction.

### Authentication & Access Control
- `sdk/cliproxy/auth/` - Core auth manager (`coreauth.Manager`) handles credential rotation, quota tracking
- `sdk/access/` - Request-level authentication (API keys, bearer tokens)
- `internal/api/server.go:722-746` - AuthMiddleware: validates requests before handler execution

Multi-layer auth enables: (1) client authentication to ModelGate, (2) ModelGate authentication to upstream providers.

### Translators
- `sdk/translator/registry.go:9-14` - Registry maps Format→Format with request/response transforms
- `internal/translator/` - Provider-specific translators (codex/, claude/, gemini/, openai/)
- `internal/translator/init.go` - Auto-registers all built-in translators

Translators enable clients using OpenAI SDK to transparently access Claude or Gemini backends by converting payloads bidirectionally.

### Executors
- `internal/runtime/executor/codex_executor.go:30-35` - CodexExecutor: credential injection, HTTP execution
- Each provider has its executor: `antigravity_executor.go`, `gemini_cli_executor.go`, `github_copilot_executor.go`, etc.

Executors are stateless—they receive an auth object and request, inject credentials, and return the response. This separation allows the same auth to be reused across retries.

### Model Registry
- `internal/registry/model_registry.go:18-73` - ModelInfo and ModelRegistration structs
- `internal/registry/model_registry.go:87-99` - ModelRegistry: tracks available models with reference counting

The registry dynamically tracks which models are available based on active client credentials. When a credential expires or quota is exceeded, models are automatically hidden from `/v1/models`.

### Configuration & Hot-Reload
- `internal/config/` - Config struct and YAML loading
- `internal/watcher/` - File system monitoring for config and auth changes
- `internal/api/server.go:665-705` - UpdateClients: applies config changes without restart

Hot-reload enables adding/removing API keys and changing settings without downtime—critical for production deployments.

## Directory Structure

```
cmd/
└── server/
    └── main.go          # Entry point, CLI flags, bootstrap

internal/
├── api/                 # HTTP server, middleware, routes
│   ├── server.go        # Core server implementation
│   ├── handlers/        # Management API handlers
│   ├── middleware/      # Request logging, auth
│   └── modules/         # Extensible route modules (amp/)
├── auth/                # Provider-specific auth (codex/, gemini/)
├── config/              # Configuration loading and validation
├── registry/            # Dynamic model registry with quota tracking
├── runtime/executor/    # Provider executors (codex, gemini-cli, etc.)
├── translator/          # Request/response format converters
├── watcher/             # File system monitoring for hot-reload
└── store/               # Token persistence (postgres, git, object storage)

sdk/
├── api/handlers/        # OpenAI/Claude/Gemini API handlers
│   ├── openai/          # /v1/chat/completions, /v1/models
│   ├── claude/          # /v1/messages (Claude format)
│   └── gemini/          # /v1beta/models (Gemini format)
├── cliproxy/            # Core service, builder, auth manager
│   ├── service.go       # Service lifecycle management
│   ├── builder.go       # Fluent construction API
│   └── auth/            # Runtime auth with quota/cooldown
├── translator/          # Format translation registry and pipeline
├── access/              # Request authentication providers
└── auth/                # Token store and authenticators
```

## Design Decisions

**Why Builder Pattern for Service?**
External applications may embed ModelGate with custom providers or auth backends. The builder allows dependency injection without modifying core code (`sdk/cliproxy/builder.go:65-71`).

**Why Separate Translators from Executors?**
Translators handle format conversion (stateless, pure transforms). Executors handle HTTP mechanics and credential injection. This separation allows mixing: an OpenAI-format request can route to Codex executor after translation.

**Why Dynamic Model Registry?**
Clients querying `/v1/models` should only see models they can actually use. The registry tracks which credentials support which models and hides unavailable ones (`internal/registry/model_registry.go:87-99`).

**Why Multiple Token Stores?**
Different deployments need different persistence: local files for development, PostgreSQL for multi-instance production, Git for GitOps workflows, S3-compatible object storage for serverless (`cmd/server/main.go:118-150`).
