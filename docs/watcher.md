# Hot-Reload System

ModelGate includes a file watcher that enables hot-reload of configuration and authentication without server restart.

## Purpose

The watcher monitors filesystem changes and applies updates live:
- Configuration changes take effect immediately
- Auth credentials refresh without downtime
- No manual restart required after editing `config.yaml` or auth files

## Watched Files

The watcher monitors three locations (`watcher.go:77-88`, `events.go:39-60`):

| Path | Purpose |
|------|---------|
| `config.yaml` | Main configuration file |
| `auths/` directory | JSON auth credential files |
| `~/.aws/sso/cache/` | Kiro IDE token files (if exists) |

Only `.json` files in the auth directory trigger reload events.

## Event Types

Auth updates use three action types (`watcher.go:57-61`):

| Action | Description |
|--------|-------------|
| `add` | New auth file detected |
| `modify` | Existing auth file changed |
| `delete` | Auth file removed |

Events are dispatched through a queue system (`dispatcher.go:127-166`):
1. Updates are batched and deduplicated by auth ID
2. Pending updates stored in order
3. Dispatch loop sends to registered consumers

## Config Change Detection

Config reload uses SHA-256 hash comparison (`config_reload.go:38-62`):

1. File write/create/rename triggers `scheduleConfigReload()`
2. Debounce delay: 150ms (`watcher.go:67`)
3. Hash computed and compared to previous
4. If unchanged, reload skipped
5. If changed, full config reload triggered

Material changes detected by `diff.BuildConfigChangeDetails()` (`config_reload.go:95-103`):
- Debug mode toggle
- Auth directory path
- Force model prefix
- OAuth model mappings
- OAuth excluded models

After reload, `reloadCallback` notifies the server (`config_reload.go:112`).

## Auth File Updates

Auth files are processed incrementally (`events.go:90-118`, `clients.go:93-138`):

### Add/Update Flow
1. Read file and compute SHA-256 hash
2. Compare to cached hash in `lastAuthHashes`
3. If unchanged, skip processing
4. Update hash cache and call `refreshAuthState()`
5. Trigger `reloadCallback` and persist changes

### Remove Flow
1. Debounce check (1 second window) to handle atomic renames (`events.go:137-157`)
2. Wait 50ms for replace operations to settle (`watcher.go:66`)
3. If file reappears, treat as update instead
4. Otherwise, remove from hash cache and refresh state

### Hash-Based Deduplication
Each auth file hash is tracked (`clients.go:60-78`):
```
lastAuthHashes[normalizedPath] = sha256(fileContent)
```
This prevents redundant reloads from duplicate filesystem events.

## Debouncing

Multiple mechanisms prevent event storms:

| Debounce | Duration | Purpose |
|----------|----------|---------|
| Config reload | 150ms | Coalesce rapid config writes |
| Auth remove | 1 second | Handle atomic replace patterns |
| Replace check | 50ms | Allow rename operations to complete |

## Runtime Auth Updates

External sources (e.g., WebSocket) can push auth updates via `DispatchRuntimeAuthUpdate()` (`dispatcher.go:41-72`):
- Updates stored in `runtimeAuths` map
- Merged with file-based auths in `refreshAuthState()`
- Same dispatch queue used for all update types

## Persistence

When a `storePersister` is available (`clients.go:202-228`):
- Config changes trigger `PersistConfig()`
- Auth changes trigger `PersistAuthFiles()` with commit message
- Operations run asynchronously with 30-second timeout

## Cross-Platform Support

Path normalization handles OS differences (`events.go:132-144`):
- Windows: Removes `\\?\` prefix, lowercases paths
- All platforms: Uses `filepath.Clean()` for consistent comparison
