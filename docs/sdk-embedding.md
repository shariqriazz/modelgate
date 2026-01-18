# Embedding ModelGate as a Go Library

## Module Import

```go
import (
    "github.com/shariqriazz/modelgate/sdk/cliproxy"
    "github.com/shariqriazz/modelgate/sdk/config"
)
```

Module path: `github.com/shariqriazz/modelgate` (no version suffix). See `go.mod:1`.

## Builder Pattern

The SDK uses a builder pattern for service construction (`sdk/cliproxy/builder.go:17-48`).

```go
cfg, _ := config.LoadConfig("/path/to/config.yaml")
svc, _ := cliproxy.NewBuilder().
    WithConfig(cfg).
    WithConfigPath("/path/to/config.yaml").
    Build()
svc.Run(context.Background())
```

## Configuration

### Required (`builder.go:145-150`)

| Method | Description |
|--------|-------------|
| `WithConfig(*config.Config)` | Application configuration object |
| `WithConfigPath(string)` | Absolute path to config file (for reload watching) |

Load config via `config.LoadConfig()` or `config.LoadConfigOptional()` (`sdk/config/config.go:43-47`).

### Optional (`builder.go:95-137`)

| Method | Description |
|--------|-------------|
| `WithTokenClientProvider()` | Custom token-based client loading |
| `WithAPIKeyClientProvider()` | Custom API key-based client loading |
| `WithWatcherFactory()` | Custom file watcher implementation |
| `WithAuthManager()` | Override authentication manager |
| `WithRequestAccessManager()` | Override request access manager |
| `WithCoreAuthManager()` | Override runtime auth manager |
| `WithServerOptions()` | HTTP server customization |
| `WithLocalManagementPassword()` | Localhost-only management password |

## Lifecycle Hooks

Register hooks via `WithHooks()` (`builder.go:49-60`):

```go
cliproxy.NewBuilder().
    WithHooks(cliproxy.Hooks{
        OnBeforeStart: func(cfg *config.Config) { /* modify config before start */ },
        OnAfterStart:  func(svc *cliproxy.Service) { /* register plugins */ },
    })
```

| Hook | Timing | Use Case |
|------|--------|----------|
| `OnBeforeStart` | Before server init | Config modification, logging setup |
| `OnAfterStart` | After server starts | Register usage plugins, health checks |

## Server Options

Customize HTTP server via `WithServerOptions()` (`internal/api/server.go:55-108`):

| Option | Description |
|--------|-------------|
| `WithMiddleware()` | Add Gin middleware |
| `WithEngineConfigurator()` | Modify Gin engine before setup |
| `WithRouterConfigurator()` | Add routes after default registration |
| `WithLocalManagementPassword()` | Localhost management authentication |
| `WithKeepAliveEndpoint()` | Enable keep-alive with timeout |
| `WithRequestLoggerFactory()` | Custom request logging |

## Service Lifecycle

### Running

`Run()` blocks until context cancellation (`service.go:385-414`):

```go
ctx, cancel := context.WithCancel(context.Background())
go func() { <-signalChan; cancel() }()
svc.Run(ctx)
```

### Shutdown

Shutdown is automatic on context cancellation with 30s timeout (`service.go:393-399`).

For manual shutdown (`service.go:578`):

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
svc.Shutdown(ctx)
```

Shutdown is idempotent (`service.go:583`). Sequence (`service.go:578-620`):
1. Cancel watcher context
2. Stop core auth manager auto-refresh
3. Stop file watcher
4. Stop websocket gateway
5. Stop auth update queue
6. Stop HTTP server (30s timeout)

## Usage Plugins

Register usage monitoring via `svc.RegisterUsagePlugin(plugin)` (`service.go:92-96`).

## Provider Interfaces

Custom providers implement these interfaces (`types.go:17-62`):
- `TokenClientProvider` - Load token-backed clients
- `APIKeyClientProvider` - Load API key-backed clients
- `WatcherFactory` - Create file watchers

Default implementations provided if not overridden (`builder.go:152-165`).
