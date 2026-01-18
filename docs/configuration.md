# Configuration

ModelGate uses YAML configuration files for all settings.

## Configuration File Location

The default configuration file is `config.yaml` in the working directory. The server also supports `config.server.yaml` for server-specific overrides.

## Configuration Sections Overview

| Section | Purpose | Key Options |
|---------|---------|-------------|
| Server | Network binding and TLS | `host`, `port`, `tls` |
| Authentication | Client API keys and auth directory | `api-keys`, `auth-dir` |
| Remote Management | Management API access control | `remote-management` |
| Providers | API key configs for various LLM providers | `gemini-api-key`, `codex-api-key`, `claude-api-key`, etc. |
| Routing | Credential selection and retry behavior | `routing`, `request-retry`, `quota-exceeded` |
| OAuth | Model mappings and exclusions for OAuth providers | `oauth-model-mappings`, `oauth-excluded-models` |
| Streaming | SSE keep-alives and bootstrap retries | `streaming` |
| Logging | Debug and file logging options | `debug`, `logging-to-file` |

## Server Settings

Basic server configuration (`config.go:24-31`):

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `host` | string | `""` | Network interface to bind. Empty binds all interfaces |
| `port` | int | `4091` | Server port |
| `tls.enable` | bool | `false` | Enable HTTPS |
| `tls.cert` | string | `""` | Path to TLS certificate file |
| `tls.key` | string | `""` | Path to TLS private key file |

## Authentication

Client authentication settings (`config.go:35`, `sdk_config.go:20`):

| Option | Type | Description |
|--------|------|-------------|
| `api-keys` | list | List of API keys for client authentication |
| `auth-dir` | string | Directory for OAuth token files (supports `~` expansion) |
| `ws-auth` | bool | Enable authentication for WebSocket API (`/v1/ws`) |

## Remote Management

Management API configuration (`config.go:33`, `config.go:122-136`):

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `remote-management.allow-remote` | bool | `false` | Allow non-localhost management access |
| `remote-management.secret-key` | string | `""` | Management API key (empty disables management API) |
| `remote-management.disable-control-panel` | bool | `false` | Disable bundled management UI |
| `remote-management.panel-github-repository` | string | See default | GitHub repo for management panel assets |

## Provider Configuration

Each provider supports common credential options (`config.example.yaml:86-111`):

| Option | Description |
|--------|-------------|
| `api-key` | The API key for authentication |
| `prefix` | Optional prefix for model routing (e.g., `test/gemini-3-pro`) |
| `base-url` | Custom API endpoint URL |
| `headers` | Custom HTTP headers as key-value pairs |
| `proxy-url` | Per-credential proxy override |
| `models` | Model name/alias mappings |
| `excluded-models` | Models to exclude (supports wildcards: `*-preview`, `gemini-*`) |

### Supported Providers

| Config Key | Provider | Reference |
|------------|----------|-----------|
| `gemini-api-key` | Google Gemini | `config.go:68` |
| `codex-api-key` | OpenAI Codex | `config.go:77` |
| `claude-api-key` | Anthropic Claude | `config.go:80` |
| `openai-compatibility` | OpenAI-compatible APIs | `config.go:83` |
| `vertex-api-key` | Vertex AI-compatible | `config.go:87` |
| `kiro` | AWS CodeWhisperer/Amazon Q | `config.go:71` |
| `ampcode` | Amp CLI integration | `config.go:90` |

## Routing and Retry Behavior

Credential routing configuration (`config.go:60-66`):

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `routing.strategy` | string | `round-robin` | Credential selection: `round-robin` or `fill-first` |
| `request-retry` | int | `3` | Retry count for failed requests (codes 403, 408, 500, 502, 503, 504) |
| `max-retry-interval` | int | `30` | Max wait seconds for cooled-down credentials |
| `quota-exceeded.switch-project` | bool | `true` | Auto-switch project on quota exceeded |
| `quota-exceeded.switch-preview-model` | bool | `true` | Auto-switch to preview model on quota exceeded |
| `force-model-prefix` | bool | `false` | Require explicit model prefixes for prefixed credentials |

## Streaming Configuration

SSE and streaming behavior (`sdk_config.go:26-27`, `sdk_config.go:33-43`):

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `streaming.keepalive-seconds` | int | `0` | SSE heartbeat interval (0 disables) |
| `streaming.bootstrap-retries` | int | `0` | Retries before first byte sent |
| `nonstream-keepalive-interval` | int | `0` | Blank line interval for non-streaming responses |

## OAuth Model Mappings

Global model mappings for OAuth providers (`config.go:98-105`). Supported channels: `gemini-cli`, `vertex`, `aistudio`, `antigravity`, `claude`, `codex`, `qwen`, `iflow`, `kiro`, `github-copilot`. Configure via `oauth-model-mappings` with `name`, `alias`, and optional `fork` (keep original) fields.

## Logging and Debug

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `debug` | bool | `false` | Enable debug logging |
| `logging-to-file` | bool | `false` | Write logs to rotating files instead of stdout |
| `logs-max-total-size-mb` | int | `0` | Max log directory size in MB (0 disables cleanup) |
| `usage-statistics-enabled` | bool | `false` | Enable in-memory usage statistics |

## Other Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `proxy-url` | string | `""` | Global outbound proxy (socks5/http/https) |
| `commercial-mode` | bool | `false` | Reduce per-request memory overhead |
| `incognito-browser` | bool | `false` | Open OAuth URLs in private browsing |
| `disable-cooling` | bool | `false` | Disable quota cooldown scheduling |

## Payload Configuration

Override or set default parameters per model (`config.example.yaml:195-206`):

```yaml
payload:
  default:  # Set only when missing
    - models:
        - name: "gemini-2.5-pro"
          protocol: "gemini"
      params:
        "generationConfig.thinkingConfig.thinkingBudget": 32768
  override:  # Always override
    - models:
        - name: "gpt-*"
      params:
        "reasoning.effort": "high"
```

## Environment Variables

The configuration is primarily file-based. Provider credentials can be stored in files under `auth-dir` (default: `~/.modelgate`) for OAuth-based authentication flows.
