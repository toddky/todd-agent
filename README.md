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

Prompt caching is always on: every request marks cache breakpoints on the tool array and the growing message history, so a caching-aware proxy (e.g. litellm to Anthropic) reuses the unchanged prefix instead of reprocessing it every turn.

## Project Layout

See the Directory Structure section in `AGENTS.md` (`CLAUDE.md` is a symlink to it).

## Agents

Each subdirectory under `agents/` is a self-contained agent definition, named after the agent:

```text
agents/
└── disk-cleaner/
    ├── tools/
    │   ├── list_dir            # symlink to the top-level tools/list_dir
    │   ├── list_dir_with_size  # list a directory with per-entry disk usage
    │   └── write_cleanup_script  # write ./cleanup.sh for the user to review and run; the agent's only write, it never deletes
    ├── prompt.md            # the agent's user/task prompt
    └── system_prompts/
        ├── logs.md          # how to check if a log file is safe to delete
        └── policy.md        # retention policy the agent should follow when deleting files
```

- `tools/` — executable tool scripts scoped to this agent, following the same tool contract as the top-level `tools/` directory.
- `prompt.md` — the prompt describing the task this agent runs.
- `system_prompts/<files>.md` — one or more system prompt fragments loaded for this agent.
