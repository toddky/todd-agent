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
│   │   ├── agent_test.go # turn loop + exit tool tests, driven by a fake ResponseStreamer
│   │   ├── session.go   # message history, session state
│   │   ├── event.go     # event types: TextDelta, ToolCallStarted, ToolResult, TurnComplete, Error
│   │   ├── tool.go      # tool discovery + dispatch: execs scripts from tools/
│   │   └── tool_test.go # tool contract tests against fake scripts in a tempdir
│   ├── llm/
│   │   ├── llm.go       # LLM API client (OpenAI Chat Completions wire format, litellm-compatible)
│   │   └── llm_test.go  # table-driven tests for the wire-format translators
│   └── ui/              # frontends; consume engine events, never imported by the engine
│       ├── repl/
│       │   └── repl.go  # plain line REPL frontend
│       └── tui/         # full-screen TUI frontend (later)
├── tools/               # executable tool scripts, any language (see Tool Contract)
│   ├── read_file        # read a file's contents
│   ├── write_file       # create or overwrite a file, creating parent dirs
│   ├── edit_file        # exact-string replacements (python3); each old_text must match exactly once
│   ├── bash             # run a shell command
│   ├── grep             # regex search, file:line:text output (rg-first, grep -rn fallback)
│   ├── glob             # find files by glob pattern (rg-first, find fallback)
│   ├── list_dir         # list a directory as '<type> <name>' lines: f=file, d=directory, l=symlink
│   ├── slack.rb         # shared Slack helpers (require_relative'd; not executable, so discovery skips it)
│   ├── slack_read_channel      # read a conversation's recent history (channels, DMs, group DMs)
│   ├── slack_read_thread       # read a thread: parent message plus replies
│   ├── slack_send_message      # post a message or thread reply
│   ├── slack_search_channels   # find channels by name (public and private)
│   ├── slack_search_users      # find users by name, display name, or email
│   ├── slack_read_user_profile # read a user's profile (defaults to the current user)
│   ├── glean.rb         # shared Glean helpers (require_relative'd; not executable, so discovery skips it)
│   ├── glean_search            # ask Glean Assistant chat a question
│   ├── glean_read_document     # read Glean-indexed documents by URL
│   └── ...
└── docs/
    └── examples/        # reference notes on how other coding agents define tools and hooks
```

Import direction is one-way: `ui` imports `agent`; `agent` imports `llm` and `tool`; nothing imports `ui`.
Tool calls exec the matching script in `tools/` so tool behavior can change while an agent is running.
Directories are created when code lands in them, not before.

On startup the agent symlinks every tool script into a private per-instance directory
(`$XDG_RUNTIME_DIR/agent-<pid>/tools`) and loads its tool registry from there, so different
agent instances can run with different tool sets. The directory is removed on exit.

## Agent Exit Codes

The `todd-agent` process itself exits with:

- `0` = success.
- `3` = runtime error: bad flags, missing API key, tool discovery failure, or API failure.
- Any other code = the model called the internal `exit` tool (see `--allow-exit`).

Passing `--allow-exit` advertises an internal `exit` tool (`{code, reason}`) to the model.
The model chooses the exit code itself; the agent uses it verbatim, so scripts can branch on model decisions.
When called, the agent stops the turn at once, prints the reason, runs cleanup, and exits with that code.
The schema documents 0-125 but nothing enforces the range: exit codes are 8-bit, so out-of-range values are truncated by the OS (e.g. 300 becomes 44), and a model picking 3 is indistinguishable from a runtime error.
The engine dispatches `exit` itself; it is never exec'd from the tools dir.
Without the flag the tool is not advertised, so the model cannot end the process.

These are distinct from the tool script exit codes below: tool codes go to the model, agent codes go to the calling shell.

## Prompt Caching

The `internal/llm` client speaks the OpenAI Chat Completions wire format, but it drives Anthropic's native prompt caching through a litellm-style proxy that translates the request. Caching is always on: it only lowers cost, so there is no flag or env var to turn it off. All the knobs live in one place, the `anthropicCache` struct (`enabled`, `type` = `"ephemeral"`, `ttl` = `"5m"`), and the wire marker `anthropicCacheMarker` is built from it.

The client places two `cache_control` breakpoints per request:

- The last tool definition in the `tools` array, so Anthropic caches the whole (rarely-changing) tool schema block.
- The last wire message, so Anthropic caches the growing history through that point and reuses it on the next turn.

Anthropic keys caching on content blocks, not bare strings, so the message breakpoint needs somewhere to attach:

- A normal message's flat-string content is switched to a `[]apiContentPart` with the marker on the text part.
- A tool-role message keeps its flat-string content (litellm's Anthropic translation requires it) and takes the marker at message level instead, where litellm copies it onto the outer Anthropic block. This matters because the last message in the agent loop is usually a tool result; marking only content parts would leave that turn uncached.

## Tool Contract

Every script in `tools/` must follow this contract (see `tools/read_file` for the reference implementation):

- `--schema` as the first argument prints a JSON self-description and exits 0. The object has
  `description`, `input_schema` (JSON Schema for the call arguments), and an optional `timeout_secs`
  (the default deadline; the agent applies its own default when it is omitted). The registry discovers tools by running `--schema` across `tools/*`.
  The registry injects an optional `timeout_secs` integer into every advertised `input_schema`, so the model can override the per-call deadline; tool authors do not declare it.
- A normal call receives JSON arguments on stdin and writes its result text to stdout.
- Failure reasons go to stderr, never stdout.
- Exit codes: `0` = success, `1` = runtime failure (e.g. file not found), `2` = malformed call (bad or missing arguments).
- The agent enforces the timeout around the exec and hard-kills a tool that overruns its deadline. A tool may optionally self-time below that deadline to return partial results, but must not depend on any grace after it.
- Never pass tool input to a shell reparse (`eval`, `bash -c`); expand paths with facilities that treat the input as data.
  Exception: `tools/bash`, where the command IS the payload and shell interpretation is the feature.
- Error messages echo the original input (e.g. the unexpanded path), never expanded values, so secret env vars cannot leak into model-visible output.

Conventions the current tools follow beyond the hard contract:

- Scripts are organized with SCHEMA / PARSE / MAIN / RESULTS section header banners.
- Path inputs are expanded with Python's `expanduser`/`expandvars`, never a shell reparse.
- Search tools (`grep`, `glob`) cap output at 200 lines/paths (`max_results`) and tell the model to narrow when truncated.
- A long-running tool self-times by wrapping its work in `timeout`, reading the call's `timeout_secs` from its input and falling back to its `--schema` default; it then prints partial results with a note instead of being killed. The wrapper is skipped if the budget is under 1s.
- `rg` is preferred and runs with `--no-ignore` so it does not honor .gitignore (it still skips hidden files, including .git); the fallback (`find`, `grep -rn`) sees everything including hidden files, so results can still differ between hosts.
- `list_dir` always includes dotfiles: the consumer is a model, and hiding .gitignore/.github/etc would misrepresent the directory.
- `edit_file` requires each `old_text` to match exactly once; edits apply in order against the evolving content.
- Tool results with empty stdout reach the model as "(no output)": the wire client substitutes it so omitempty cannot drop the content field, which the proxy rejects.
- A tool that modifies a file writes to a temp file in the same directory and atomically renames it, so a crash mid-write leaves the original intact.
- Tools guard at their input boundary and refuse rather than corrupt: reject binary or oversized input, and reject a call that would change nothing.
- Python tool scripts declare and enforce a minimum interpreter version in a `VERSION` section; keep it as low as the syntax used allows (currently 3.6).

## Dependencies

- External dependencies: `jq` (tool scripts parse their JSON input with it) and `python3` (path expansion in the shell tools, and the interpreter for `edit_file`, which requires 3.6+).
- The only external Go libraries are `github.com/charmbracelet/bubbletea` and `github.com/charmbracelet/bubbles`, used for the REPL's text input.
- Do not add any external library without permission from the user.
- Never use an external SDK for AI or LLM APIs. The `internal/llm` package speaks the wire format directly with `net/http`.

## Naming and API Style

- Keep each package's exported API as small as possible. Fold helper steps into the function that needs them instead of exporting them (e.g. tool discovery lives inside `Setup`, not a separate exported function).
- For a process-scoped singleton, prefer short lifecycle names paired as `Setup`/`Cleanup` over descriptive compounds like `SetupRuntimeTools`/`CleanupRuntimeTools`.
- Package state is acceptable for something that exists exactly once per process (like the runtime dir); expose it through a getter instead of threading it through every call site.
- Getter functions start with `Get` (e.g. `GetRuntimeDir`).
- Name enum-ish fields `Type`, matching the wire format's naming (`ContentBlock.Type`, `Event.Type`), not `Kind`.
- Inside a package, name the one-item helper after the verb and the collection loader `<Verb>All` (e.g. `load` and `LoadAll`).
- Never use the word `emit` (or any inflection) in identifiers, comments, or commit messages. Use `notify`, `publish`, `write`, or `print` instead.
- Comments are one sentence per line, max 2 lines. Explain arbitrary numbers (timeouts, sizes, caps): say where the value came from, or that it is a guess.

## Testing

- Tests are stdlib `testing` only, table-driven, in `foo_test.go` beside `foo.go`, same package so unexported functions are reachable.
- Run everything with `go test ./...`; there is no separate test directory or framework.
- Pure functions get tests first (e.g. the wire-format translators in `internal/llm`); exec- and network-dependent code needs fakes (tempdir tool scripts, `httptest`) and comes later.
- The agent loop is tested through the `ResponseStreamer` interface (`Agent.Client`): tests inject a scripted fake instead of the real HTTP client.
- Tool contract tests write throwaway tool scripts into `t.TempDir()` and run discovery/dispatch against them; no fixtures are checked in.

## Operational Rules

- Prefer early returns over deep nesting.
- Write code that can be understood without referencing other files. Be explicit rather than clever.
- Use descriptive names for things (the tool registry, the agent loop), not exact file paths, since layout will move early on.
- Only add a rule to this file if it changes agent behavior. Do not describe things the model already knows from training (e.g. what `src/` is for).

