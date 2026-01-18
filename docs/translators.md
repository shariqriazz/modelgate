# Translation System

ModelGate's translation system enables seamless conversion between different AI provider API formats, allowing clients to use their preferred API format while routing requests to any supported backend provider.

## Purpose

The translation layer converts:
- **Requests**: Client format (e.g., OpenAI) -> Provider format (e.g., Claude)
- **Responses**: Provider format -> Client format (streaming and non-streaming)

This allows a client using the OpenAI SDK to transparently access Claude, Gemini, or other providers.

## Supported Formats

Defined in `sdk/translator/formats.go:4-11`:

| Format | Identifier | Description |
|--------|------------|-------------|
| OpenAI Chat Completions | `openai` | Standard OpenAI `/v1/chat/completions` format |
| OpenAI Responses | `openai-response` | OpenAI `/v1/responses` endpoint format |
| Claude | `claude` | Anthropic Messages API format |
| Gemini | `gemini` | Google Gemini API format |
| Gemini CLI | `gemini-cli` | Gemini CLI-specific format |
| Codex | `codex` | OpenAI Codex format |
| Antigravity | `antigravity` | Antigravity-specific format |

## Architecture

### Core Types

**`sdk/translator/types.go`**:
- `RequestTransform` (line 8): Function signature for request conversion
- `ResponseStreamTransform` (line 13): Streaming response converter
- `ResponseNonStreamTransform` (line 18): Non-streaming response converter
- `ResponseTransform` (line 23): Groups stream/non-stream/token-count transforms

### Registry Pattern

The registry (`sdk/translator/registry.go`) stores bidirectional translation functions:

- `Register()` (line 22): Registers request and response transforms for a format pair
- `TranslateRequest()` (line 39): Converts request payloads between formats
- `TranslateStream()` (line 60): Converts streaming response chunks
- `TranslateNonStream()` (line 72): Converts complete responses

A default global registry is exposed via `Default()` (line 93) for shared use across the application.

### Pipeline

The pipeline (`sdk/translator/pipeline.go`) provides middleware-enabled translation:

- `RequestEnvelope` / `ResponseEnvelope` (lines 5-17): Wrap payloads with metadata
- `Pipeline.TranslateRequest()` (line 54): Applies middleware chain then registry transform
- `Pipeline.TranslateResponse()` (line 71): Handles streaming vs non-streaming responses

Middleware functions (`RequestMiddleware`, `ResponseMiddleware`) can intercept and modify translations.

## Translation Flow

```
1. Client Request (Format A)
   │
   ├─→ TranslateRequest(from=A, to=B)
   │   └─→ Lookup registry.requests[A][B]
   │   └─→ Apply transform function
   │
2. Translated Request (Format B) → Provider
   │
3. Provider Response (Format B)
   │
   ├─→ TranslateStream/TranslateNonStream(from=B, to=A)
   │   └─→ Lookup registry.responses[A][B]
   │   └─→ Apply transform function
   │
4. Translated Response (Format A) → Client
```

## Directory Structure

Translator implementations live under `internal/translator/`:

```
internal/translator/
├── init.go                    # Imports all translators to trigger registration
├── translator/translator.go   # Internal wrapper around SDK registry
├── openai/                    # Translations FROM OpenAI format
│   ├── claude/                # OpenAI → Claude provider
│   ├── gemini/                # OpenAI → Gemini provider
│   ├── gemini-cli/            # OpenAI → Gemini CLI
│   └── openai/                # OpenAI → OpenAI (passthrough/endpoint variants)
│       ├── chat-completions/  # /v1/chat/completions endpoint
│       └── responses/         # /v1/responses endpoint
├── claude/                    # Translations FROM Claude format
│   ├── openai/
│   ├── gemini/
│   └── gemini-cli/
├── gemini/                    # Translations FROM Gemini format
├── gemini-cli/                # Translations FROM Gemini CLI format
├── codex/                     # Translations FROM Codex format
└── antigravity/               # Translations FROM Antigravity format
```

### Naming Convention

Each translator package contains:
- `init.go`: Registers the translator via `init()` function
- `{source}_{target}_request.go`: Request transformation logic
- `{source}_{target}_response.go`: Response transformation logic (stream + non-stream)

Example: `internal/translator/openai/claude/init.go:9-19` registers OpenAI↔Claude translation.

## Registration

Translators self-register via Go's `init()` mechanism. The master import file `internal/translator/init.go` imports all translator packages, ensuring registration occurs at startup.

Registration example from `internal/translator/openai/claude/init.go:9-19`:
```
translator.Register(
    Claude,                    // from format
    OpenAI,                    // to format  
    ConvertClaudeRequestToOpenAI,
    interfaces.TranslateResponse{
        Stream:     ConvertOpenAIResponseToClaude,
        NonStream:  ConvertOpenAIResponseToClaudeNonStream,
        TokenCount: ClaudeTokenCount,
    },
)
```

## Adding a New Translator

1. Create directory: `internal/translator/{source}/{target}/`
2. Implement request transform function matching `RequestTransform` signature
3. Implement response transform functions (stream and non-stream)
4. Create `init.go` that calls `translator.Register()`
5. Add blank import to `internal/translator/init.go`
