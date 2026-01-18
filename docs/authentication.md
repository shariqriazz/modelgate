# Authentication

ModelGate's authentication system manages credentials for multiple AI providers, handling OAuth flows, token storage, and automatic refresh.

## Architecture Overview

The authentication system consists of three layers:

1. **Core Auth Types** (`sdk/cliproxy/auth/types.go:15-60`) - The `Auth` struct stores credential state including provider, tokens (in `Metadata`), status, quota tracking, and per-model availability via `ModelStates`.

2. **Manager** (`sdk/cliproxy/auth/conductor.go:105-145`) - Orchestrates auth lifecycle: registration, selection, execution delegation, and persistence. Runs background refresh loops.

3. **Authenticators** (`sdk/auth/interfaces.go:22-27`) - Provider-specific login implementations. Each authenticator implements `Provider()`, `Login()`, and `RefreshLead()` methods.

## Token Storage

### FileStore

The `FileTokenStore` (`sdk/auth/filestore.go:18-26`) persists credentials as JSON files on disk:

- **Save** (`sdk/auth/filestore.go:37-103`) - Writes auth records to `{authDir}/{provider}/{filename}.json`
- **List** (`sdk/auth/filestore.go:107-131`) - Scans auth directory for all `.json` files
- **Delete** (`sdk/auth/filestore.go:134-145`) - Removes credential files by ID

### Directory Structure

```
auths/
├── codex/
│   └── account@email.json
├── gemini/
│   └── project-123.json
├── github-copilot/
│   └── user.json
└── antigravity/
    └── session.json
```

The `SetBaseDir()` method (`sdk/auth/filestore.go:29-33`) configures the root auth directory (default from `config.AuthDir`).

## OAuth Flows

### Codex (OpenAI)

OAuth2 + PKCE flow (`internal/auth/codex/openai_auth.go:27-75`):

1. Generate PKCE codes (code verifier + challenge)
2. Build auth URL with `code_challenge_method=S256`
3. User authorizes via browser at `auth.openai.com`
4. Local callback server captures authorization code
5. Exchange code for tokens at `/oauth/token`

### Registered Providers

Provider authenticators are registered at init (`sdk/auth/refresh_registry.go:9-16`):

| Provider | Authenticator |
|----------|---------------|
| codex | `NewCodexAuthenticator()` |
| qwen | `NewQwenAuthenticator()` |
| iflow | `NewIFlowAuthenticator()` |
| gemini | `NewGeminiAuthenticator()` |
| gemini-cli | `NewGeminiAuthenticator()` |
| antigravity | `NewAntigravityAuthenticator()` |
| github-copilot | `NewGitHubCopilotAuthenticator()` |

### Login Flow

The `Manager.Login()` method (`sdk/auth/manager.go:44-71`):

1. Looks up authenticator by provider name
2. Calls `authenticator.Login()` to run OAuth flow
3. Persists resulting `Auth` record via the store
4. Returns auth record and saved file path

## Token Refresh

### Automatic Refresh

The Manager runs a background refresh loop (`sdk/cliproxy/auth/conductor.go:47-48`):

- **Check interval**: 5 seconds (`refreshCheckInterval`)
- **Pending backoff**: 1 minute (`refreshPendingBackoff`)
- **Failure backoff**: 1 minute (`refreshFailureBackoff`)

### Refresh Lead Time

Each provider specifies how early to refresh before expiration via `RefreshLead()` (`sdk/auth/interfaces.go:26`). The `ProviderRefreshLead()` function (`sdk/cliproxy/auth/types.go:249-263`) determines timing by checking runtime evaluator first, then the registered factory.

### Expiration Detection

Token expiration is extracted from auth metadata (`sdk/cliproxy/auth/types.go:219-235`) by checking keys: `expired`, `expire`, `expires_at`, `expiry`, `expires`.

## Credential Selection

### Round-Robin Strategy

`RoundRobinSelector` (`sdk/cliproxy/auth/selector.go:17-22`):

- Maintains per-provider cursor tracking
- Rotates through available credentials evenly
- Cursor key: `{provider}:{model}`

### Fill-First Strategy

`FillFirstSelector` (`sdk/cliproxy/auth/selector.go:24-27`):

- Always selects the first available credential
- "Burns" one account before moving to next
- Useful for staggering rolling-window limits

### Priority Support

Both strategies respect the `priority` attribute (`sdk/cliproxy/auth/selector.go:90-104`):

- Higher priority credentials are preferred
- Credentials grouped by priority, best group used first
- Within same priority, sorted by ID for deterministic ordering

### Availability Filtering

Before selection, credentials are filtered (`sdk/cliproxy/auth/selector.go:106-122`):

- Excludes disabled credentials (`Status == StatusDisabled`)
- Excludes credentials in cooldown (`NextRetryAfter` in future)
- Tracks per-model availability via `ModelStates`

### Cooldown Handling

When all credentials are cooling down, returns `modelCooldownError` (`sdk/cliproxy/auth/selector.go:36-86`) with:

- HTTP 429 status
- `Retry-After` header
- Reset time information

## Quota Management

The `QuotaState` struct (`sdk/cliproxy/auth/types.go:64-73`) tracks:

- `Exceeded`: whether quota limit was hit
- `NextRecoverAt`: when credential becomes available
- `BackoffLevel`: exponential backoff multiplier

Backoff configuration (`sdk/cliproxy/auth/conductor.go:54-55`):

- Base: 1 second (`quotaBackoffBase`)
- Maximum: 30 minutes (`quotaBackoffMax`)
