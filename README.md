# ModelGate

A high-performance API gateway for AI/LLM providers with built-in authentication, load balancing, and resilience.

## Features

- **Multi-Provider Support**: Antigravity (Gemini), Codex, Qwen, IFlow, GitHub Copilot
- **OAuth Credential Management**: Automatic token refresh and secure storage
- **Load Balancing**: Round-robin and fill-first routing strategies
- **Resilience**: Configurable retries, quota-exceeded failover, streaming keep-alives
- **Management API**: Web-based control panel for configuration

## Quick Start

```bash
# Build
go build -o modelgate ./cmd/server

# Login to a provider
./modelgate -antigravity-login

# Start the server
./modelgate -config config.yaml
```

## Configuration

See [config.example.yaml](config.example.yaml) for all available options.

For server deployment, see [config.server.yaml](config.server.yaml).

## Supported Providers

| Provider | Auth Method | Status |
|----------|-------------|--------|
| Antigravity (Gemini) | OAuth | Active |
| Gemini API Key | API Key | Active |
| Codex | OAuth | Active |
| Qwen | OAuth | Active |
| IFlow | OAuth/Cookie | Active |
| GitHub Copilot | Device Flow | Active |

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
