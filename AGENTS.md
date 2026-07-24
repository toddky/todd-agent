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
├── tools/               # executable tool scripts, any language (see Tool Contract)
│   ├── read             # read a file's contents
│   └── ...
└── docs/
    └── examples/        # reference notes on how other coding agents define tools and hooks
```

Import direction is one-way: `ui` imports `agent`; `agent` imports `llm` and `tool`; nothing imports `ui`.
Tool calls exec the matching script in `tools/` so tool behavior can change while an agent is running.
Directories are created when code lands in them, not before.

## Tool Contract

Every script in `tools/` must follow this contract (see `tools/read_file` for the reference implementation):

- `--schema` as the first argument prints a JSON self-description and exits 0. The object has
  `description`, `input_schema` (JSON Schema for the call arguments), and `timeout_secs`.
  The registry discovers tools by running `--schema` across `tools/*`.
- A normal call receives JSON arguments on stdin and writes its result text to stdout.
- Failure reasons go to stderr, never stdout.
- Exit codes: `0` = success, `1` = runtime failure (e.g. file not found), `2` = malformed call (bad or missing arguments).

## Naming and API Style

- Keep each package's exported API as small as possible. Fold helper steps into the function that needs them instead of exporting them (e.g. tool discovery lives inside `Setup`, not a separate exported function).
- For a process-scoped singleton, prefer short lifecycle names paired as `Setup`/`Cleanup` over descriptive compounds like `SetupRuntimeTools`/`CleanupRuntimeTools`.
- Package state is acceptable for something that exists exactly once per process (like the runtime dir); expose it through a getter instead of threading it through every call site.
- Getter functions start with `Get` (e.g. `GetRuntimeDir`).
- Name enum-ish fields `Type`, matching the wire format's naming (`ContentBlock.Type`, `Event.Type`), not `Kind`.
- Inside a package, name the one-item helper after the verb and the collection loader `<Verb>All` (e.g. `load` and `LoadAll`).
- Never use the word `emit` (or any inflection) in identifiers, comments, or commit messages. Use `notify`, `publish`, `write`, or `print` instead.

## Operational Rules

- Prefer early returns over deep nesting.
- Write code that can be understood without referencing other files. Be explicit rather than clever.
- Use descriptive names for things (the tool registry, the agent loop), not exact file paths, since layout will move early on.
- Only add a rule to this file if it changes agent behavior. Do not describe things the model already knows from training (e.g. what `src/` is for).

