# todd-agent

A coding agent built from scratch in Go. Calls the Anthropic Messages API
directly with `net/http`; no external SDK.

## Requirements

- Go 1.23+
- An Anthropic API key (or a litellm-compatible proxy)
- `jq` (used by tool scripts to parse their JSON input)
- `python3` (optional; expands `~` and env vars in tool paths)

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

See the Directory Structure section in `AGENTS.md` (`CLAUDE.md` is a symlink to it).
