# todd-agent

A coding agent built from scratch in Go. Calls the Anthropic Messages API
directly with `net/http`; no external SDK.

## Requirements

- Go 1.23+
- An Anthropic API key

## Build

```sh
go build -o todd-agent .
```

## Run

```sh
export ANTHROPIC_API_KEY=sk-...
./todd-agent
```

Optional environment variables:

| Variable | Default | Purpose |
|----------|---------|---------|
| `ANTHROPIC_API_KEY` | (required) | API key; `ANTHROPIC_AUTH_TOKEN` is accepted as a fallback |
| `ANTHROPIC_BASE_URL` | `https://api.anthropic.com` | API endpoint, for proxies |
| `ANTHROPIC_MODEL` | `claude-sonnet-5` | Model name |

## Project Layout

- `main.go`: entry point; sends a hello-world prompt to the Messages API and prints the text response
- `docs/examples/`: reference notes on how other coding agents define tools and hooks
- `AGENTS.md`: operating instructions for coding agents working in this repo (`CLAUDE.md` is a symlink to it)
