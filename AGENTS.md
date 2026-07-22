# todd-agent Development Notes

**todd-agent** is a coding agent being built from scratch. These instructions apply to all agents working in this repository.

## Canon vs. Not Canon

This file and any `docs/` content are canon: treat them as the source of truth for how the system works today.
Plans, specs, and brainstorming notes are not canon: treat them as intent or history, not current fact.
Do not assume a plan or spec describes what is actually implemented; verify against the code.

## Directory Structure

```text
todd-agent/
├── main.go              # entrypoint: wires the engine to a frontend
├── internal/
│   ├── agent/           # engine — no terminal I/O allowed
│   │   ├── agent.go     # agent loop: send prompt, handle tool calls, iterate
│   │   ├── session.go   # message history, session state
│   │   ├── event.go     # event types: TextDelta, ToolCallStarted, ToolResult, TurnComplete, Error
│   │   └── tool.go      # tool discovery + dispatch: execs scripts from tools/
│   ├── llm/
│   │   └── llm.go       # LLM API client (Anthropic Messages wire format, litellm-compatible)
│   └── ui/              # frontends; consume engine events, never imported by the engine
│       ├── repl/
│       │   └── repl.go  # plain line REPL frontend
│       └── tui/         # full-screen TUI frontend (later)
├── tools/               # executable tool scripts, any language; JSON args on stdin, JSON result on stdout
│   ├── read_file        # read a file's contents
│   ├── write_file       # create or overwrite a file
│   ├── bash             # run a shell command
│   └── ...
└── docs/
    └── examples/        # reference notes on how other coding agents define tools and hooks
```

Import direction is one-way: `ui` imports `agent`; `agent` imports `llm` and `tool`; nothing imports `ui`.
Tool calls exec the matching script in `tools/` so tool behavior can change while an agent is running.
Directories are created when code lands in them, not before.

## Operational Rules

- Prefer early returns over deep nesting.
- Write code that can be understood without referencing other files. Be explicit rather than clever.
- Use descriptive names for things (the tool registry, the agent loop), not exact file paths, since layout will move early on.
- Only add a rule to this file if it changes agent behavior. Do not describe things the model already knows from training (e.g. what `src/` is for).

