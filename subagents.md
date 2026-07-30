# Subagents — Design Plan

## Problem Statement

`agents/<name>/` bundles a restricted tool set (`tools/`), standing instructions
(`system_prompts/*.md`), and a default task (`prompt.md`). Today an agent can only be
run as the top-level process (`--agent <name>` in main.go, lines ~100-116). There is
no way for a running agent to call another agent.

Pain points:

1. A parent agent cannot fan work out to a specialist (e.g. ask `jira-scout` a question
   mid-task). The user has to run each agent by hand and shuttle results between them.
2. Tool sets cannot compose safely. Giving the parent all of `jira-filer`'s write tools
   just to file one issue defeats the point of restricted per-agent tool directories.
3. `--agent` is all-or-nothing: one agent per process, chosen at startup, fixed for the
   process lifetime.

Current state that shapes the design:

| Fact | Where | Consequence |
|---|---|---|
| Tools are exec'd scripts from a per-process runtime dir | `agent.Setup`, tool.go | Tool isolation is directory-based and process-scoped |
| The engine already dispatches one internal tool (`exit`) itself | agent.go:95-101 | There is a pattern for engine-dispatched synthetic tools |
| `--acp` runs an agent as a long-lived JSON-RPC subagent over stdio | acp.go | The subagent side of the wire already exists and is tested |
| ACP mode ignores `prompt.md` but keeps tools and system prompts | main.go:113 | A parent-supplied task is the intended ACP usage |
| `Registry.Definitions()` is recomputed at every `Turn` call | agent.go:57 | Tool lists can grow mid-session between turns |

## Prior Art

- **Claude Code / LLM Agent `delegate`**: subagents are separate contexts advertised to
  the model as a tool; the parent passes a prompt, gets text back. Isolation and a simple
  request/response shape work well. We copy the "subagent = tool call" model.
- **ACP (Agent Client Protocol)**: exactly this use case — a parent process drives a
  child agent over JSON-RPC/stdio with persistent sessions. We already implement the
  agent side; the parent side is the missing half.
- **This repo's `exit` tool**: an internal tool dispatched inside `Turn`, never exec'd
  from the tools dir. Subagent tools follow the same dispatch path.

## Design Goals

1. A parent agent can call N named subagents, each with its own restricted tools and
   system prompts from `agents/<name>/`.
2. The model sees each subagent as an ordinary tool (`subagent_<name>`) taking a prompt.
3. A subagent keeps its conversation context across calls within one parent session.
4. `--oneshot`/CLI: `--subagent jira-scout --subagent simscope-scout` (repeatable flag).
5. REPL: `&simscope-scout` attaches a subagent mid-session.
6. No change to the tool script contract; no new external dependencies.
7. Children never outlive the parent.

## Proposals

### Proposal A: subagent as a plain tool script (`--oneshot` per call)

**Concept.** A generic `tools/subagent` script whose input is `{"agent": "...", "prompt": "..."}`.
It execs `todd-agent --agent <name> --oneshot --prompt <task>` and prints the child's answer.

**Semantics.** Stateless: every call is a fresh process with empty history. The parent
model picks the agent by name. No engine changes at all.

**Affected files.**
- **`tools/subagent`** — new script: schema, exec, print stdout.
- Everything else unchanged.

**Pros.** Trivial; fits the existing contract; shippable in an hour.
**Cons.** No context across calls (goal 3 fails); no `&name` REPL syntax (goal 5 fails);
per-call process startup (tool discovery re-runs every call); the model can name any
agent, so restricting which subagents are reachable needs extra input validation.

### Proposal B: engine-managed ACP child processes (recommended)

**Concept.** The engine holds a set of named subagents. Each is a child process running
`todd-agent --agent <name> --acp`, spoken to over stdio using the ACP subset acp.go
already implements. Each subagent is advertised to the model as a synthetic tool
`subagent_<name>` with input `{"prompt": "..."}`, dispatched inside `Turn` exactly like
the `exit` tool — never exec'd from the tools dir.

**Semantics.**
- Spawn is lazy: the child starts on the first call to its tool, then persists for the
  parent's lifetime. One `initialize` + `session/new` at spawn; one `session/prompt` per
  tool call, reusing the same sessionId so the child keeps context (goal 3).
- The child's streamed `session/update` notifications are surfaced as parent events
  (rendered gray like tool machinery), so the user can watch the subagent work.
- The parent blocks on the subagent call until its `session/prompt` response arrives;
  the final text becomes the tool result. `Turn` is already sequential, so no locking.
- `--subagent <name>` (repeatable) registers subagents at startup for any frontend.
- REPL input starting with `&` is intercepted before the model sees it: `&simscope-scout`
  attaches that subagent and prints a confirmation line. Because `Definitions()` runs per
  `Turn`, the new tool appears on the next turn with no reload machinery.
- Children are killed in `Cleanup` (SIGTERM, then kill after a short grace).

**Affected files.**
- **`internal/agent/subagent.go`** — new file:
  - `type Subagent`: name, child process handles, stdio pipes, sessionId, spawned flag.
  - `Spawn()`: exec `os.Executable()` with `--agent <name> --acp`, do initialize + session/new.
  - `Call(prompt string, notify func(Event)) (string, error)`: write session/prompt, read
    lines, forward session/update lines as events, return the final text.
  - `Definition() llm.ToolDef`: builds the `subagent_<name>` tool def (hyphens → underscores).
  - `Kill()`: terminate the child.
- **`internal/agent/agent.go`**:
  - `Agent` struct: add `Subagents map[string]*Subagent`.
  - `Turn()` (~line 57): append subagent defs after `exitToolDef`; in the tool_use loop
    (~line 95), dispatch `subagent_*` names to `Subagent.Call` before `Tools.Run`.
- **`internal/agent/subagent_test.go`** — new file: fake child script speaking canned ACP
  lines (same tempdir-fake pattern as tool_test.go).
- **`main.go`**:
  - Add repeatable `--subagent` flag (reuse `stringList`, ~line 70).
  - Validate each name resolves under `agents/` via `bundledDir`; build the Subagents map.
  - Pass the map into the engine (~line 190); kill children in the existing deferred cleanup.
- **`internal/ui/repl/repl.go`**:
  - `Run()` loop (~line 142): if the prompt starts with `&`, treat the rest as an agent
    name, attach it, print confirmation, `continue` without running a model turn.
- **`internal/ui/acp/acp.go`** — no changes; the child side works as-is (verified: it keeps
  per-session history and streams updates, which is exactly what the parent consumes).

**Pros.** Meets all seven goals; context persists; reuses the tested ACP surface; tool
restriction is structural (child process, child runtime dir); the `exit`-tool dispatch
pattern already exists in the engine.
**Cons.** Parent-side ACP client is new code (~150 lines); child process lifecycle to get
right; subagent calls are opaque token spend inside one parent turn.

### Proposal C: in-process subagents (multiple engines, one process)

**Concept.** Build a second `agent.Agent` in the same process per subagent, with its own
registry and system prompt, and call `Turn` directly — no child processes, no ACP.

**Semantics.** Same tool-facing shape as B, but `agent.Setup`/`GetRuntimeDir` are
process-scoped singletons (one runtime tools dir per process), so per-subagent tool
isolation requires reworking the runtime-dir model into per-engine instances first.

**Pros.** No process management, no wire protocol, cheapest calls.
**Cons.** Requires refactoring the package-level runtime dir/singleton design (explicitly
sanctioned in CLAUDE.md as package state); a runaway subagent shares the parent's fate;
loses the "different agent sets per process" property the symlinked runtime dir provides.

## Recommendation

| Criterion | A: tool script | B: ACP children | C: in-process |
|---|---|---|---|
| Context across calls (goal 3) | ❌ | ✅ | ✅ |
| `--subagent` + `&name` UX (goals 4-5) | ❌ | ✅ | ✅ |
| Tool isolation (goal 1) | ⚠️ input-validated | ✅ structural | ⚠️ needs refactor |
| Engine changes | ✅ none | ⚠️ moderate | ❌ large refactor |
| Reuses existing tested code | ⚠️ | ✅ acp.go as-is | ❌ |
| Process/lifecycle risk | ✅ | ⚠️ | ✅ |

**Pick B.** It is the only option meeting every goal, and both of its risks are bounded:
the ACP surface is small and already tested from the agent side, and lifecycle is
spawn-lazily/kill-on-cleanup. Separate processes are the right call here, not overhead:
tool isolation in this codebase is directory-and-process based by design, so a subagent
in its own process gets its restricted tool set for free.

## Implementation Plan

**Phase 1 — parent-side ACP client (core).**
1. `internal/agent/subagent.go`: `Subagent` struct, `Spawn`, `Call`, `Kill`, `Definition`.
   Line-delimited JSON-RPC over the child's stdin/stdout pipes; a bufio.Scanner with the
   same 16MiB cap acp.go uses.
2. `Call` reads until the response with the matching request id; `session/update` lines
   in between become `EventToolResult`-style progress events via notify.

**Phase 2 — engine dispatch.**
3. `Agent.Subagents` field; advertise defs in `Turn`; dispatch `subagent_<name>` calls
   before `Tools.Run`, mirroring the exit-tool branch.

**Phase 3 — frontends.**
4. `main.go`: `--subagent` flag, validation against `agents/`, wiring, cleanup.
5. `repl.go`: `&name` interception with a confirmation line and error for unknown names.

**Phase 4 — tests.**
6. `subagent_test.go`: fake child binary (a shell script printing canned ACP responses)
   exercising Spawn/Call/Kill and the update-forwarding path.
7. Extend `agent_test.go` with a scripted turn that calls a fake subagent tool.

## Testing Plan

- `TestSubagentCall`: canned child echoes a session/update then a response; assert the
  returned text and the forwarded events.
- `TestSubagentKeepsSession`: two `Call`s reuse one sessionId (assert child receives the
  same id twice).
- `TestSubagentChildError`: child writes a JSON-RPC error; assert `Call` surfaces it as
  a tool error, not a crash.
- `TestTurnDispatchesSubagent`: engine-level test with a fake streamer requesting
  `subagent_fake`; assert dispatch bypasses the registry.
- REPL `&name` parse: table-driven test on the interception (unknown name, empty name).

## Migration & Compatibility

- No breaking changes: without `--subagent` or `&name`, behavior is identical.
- The tool script contract is untouched.
- `--agent X --subagent Y` composes: X's tools plus a `subagent_y` tool.
- Docs: CLAUDE.md directory table gains `internal/agent/subagent.go`; flag docs in main.go.

## Open Questions

1. Should a subagent's `prompt.md` be appended to its system prompts when spawned via
   ACP, so its standing instructions still apply when the parent supplies the task?
   (Today ACP mode drops prompt.md entirely; probably yes, as an extra
   `--system-prompt-file`, but it changes what `prompt.md` means.)
2. Nesting: may a subagent itself have subagents? Nothing prevents passing `--subagent`
   in the spawn args later; proposed answer is "not in v1" to avoid runaway trees.
3. Should `&name` also support detaching (`&-name`?) or listing attached subagents? v1
   can print attached names on `&` with no argument.
4. Per-subagent model override (e.g. scouts on a cheaper model via `ANTHROPIC_MODEL` in
   the child's env)? Cheap to add at spawn time; needs a flag syntax decision
   (`--subagent name=model`?).
