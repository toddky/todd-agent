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

- `main.go`: entry point; sets up the per-instance runtime tools dir and wires the engine to the REPL
- `internal/agent/`: the engine — agent loop, tool registry/dispatch, engine events, runtime dir setup
- `internal/llm/`: Anthropic Messages API client (tool calling, streaming, retry with backoff)
- `internal/ui/repl/`: plain line REPL frontend
- `tools/`: executable tool scripts discovered via `--schema` (see AGENTS.md for the contract)
- `docs/examples/`: reference notes on how other coding agents define tools and hooks
- `AGENTS.md`: operating instructions for coding agents working in this repo (`CLAUDE.md` is a symlink to it)

On startup the agent symlinks every tool script into a private per-instance directory
(`$XDG_RUNTIME_DIR/agent-<pid>/tools`) and loads its tool registry from there, so different
agent instances can run with different tool sets. The directory is removed on exit.
