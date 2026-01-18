# Deployment Guide

ModelGate can be deployed as a binary or using Docker containers.

## Binary Deployment

Build: `go build -o modelgate ./cmd/server`

With version info (see `Dockerfile:13` for ldflags):
`go build -ldflags="-s -w -X 'main.Version=1.0.0'" -o modelgate ./cmd/server`

Run: `./modelgate -config config.yaml`

## Docker Deployment

**Recommended**: Run `./docker-build.sh` which provides interactive options (`docker-build.sh:103-108`):
1. Pre-built Image - Downloads and runs the latest release
2. Build from Source - Compiles locally with version info injected

**Direct**: `docker compose up -d`

Build arguments (`docker-compose.yml:7-10`): `VERSION`, `COMMIT`, `BUILD_DATE`

## Configuration

### Server vs Local

| Setting | Local | Server |
|---------|-------|--------|
| `host` | `127.0.0.1` | `0.0.0.0` (`config.server.yaml:10`) |
| `commercial-mode` | `false` | `true` (`config.server.yaml:46`) |
| `remote-management.allow-remote` | `false` | `true` (`config.server.yaml:24`) |
| `logging-to-file` | `false` | `true` (`config.server.yaml:66`) |

### Authentication Directory

OAuth credentials stored in auth directory (`config.server.yaml:14`):
- Default: `~/.modelgate`
- Docker mount: `/root/.modelgate` (`docker-compose.yml:16`)

Login before starting: `./modelgate -antigravity-login` or `./modelgate -codex-login`

### Port and TLS

Default port: `4091` (`Dockerfile:25`, `docker-compose.yml:14`)

TLS strongly recommended for remote access (`config.server.yaml:13-16`):
1. Generate certs: `openssl req -x509 -newkey rsa:4096 -keyout key.pem -out cert.pem -days 365 -nodes`
2. Set `tls.enable: true` and provide `tls.cert`/`tls.key` paths

### Docker Volume Mounts

Required mounts (`docker-compose.yml:15-17`):

| Host Path | Container Path | Purpose |
|-----------|----------------|---------|
| `./config.server.yaml` | `/app/config.yaml` | Configuration |
| `/root/.modelgate` | `/root/.modelgate` | OAuth credentials |
| `./logs` | `/app/logs` | Log files |

## Production Considerations

### Security

- **Change management key** - Replace default (`config.server.yaml:28`)
- **Enable TLS** - Required for remote access
- **Configure API keys** - Client authentication (`config.server.yaml:17-18`)
- **Firewall** - Restrict access when binding to `0.0.0.0`

### Performance

- `commercial-mode: true` - Reduces middleware overhead (`config.server.yaml:46`)
- `logs-max-total-size-mb` - Limits disk usage (`config.server.yaml:69`)
- `routing.strategy: round-robin` - Load distribution (`config.server.yaml:56`)

### Monitoring

- `usage-statistics-enabled: true` - Usage tracking (`config.server.yaml:72`)
- Management API at `/v0/management/` for health checks
- Export stats with `docker-build.sh --with-usage` before rebuilds

### Container Commands

- Logs: `docker compose logs -f`
- Restart: `docker compose restart`
- Stop: `docker compose down`

Container uses `restart: unless-stopped` policy (`docker-compose.yml:18`).
