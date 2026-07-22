# Gemini CLI Hooks

Reference: https://geminicli.com/docs/hooks/

## Mechanics

Hooks communicate via stdin/stdout. Only the final JSON object may be printed to stdout; any other text breaks parsing and defaults to "Allow". Use stderr for logging.

Exit codes: `0` = success (stdout parsed as JSON, including intentional blocks like `{"decision": "deny"}`); `2` = system block (aborts the action, stderr is the rejection reason); any other code = non-fatal warning, action proceeds with original parameters.

Matchers: for `BeforeTool`/`AfterTool` the `matcher` is a regex (e.g. `"write_.*"`); for lifecycle events it's an exact string (e.g. `"startup"`); `"*"` or `""` matches everything.

Hooks are configured in `settings.json` (project `.gemini/settings.json` > user `~/.gemini/settings.json` > system `/etc/gemini-cli/settings.json` > extensions). Each hook entry has `type` (only `"command"` supported), `command` (required), and optional `name`, `timeout` (ms, default 60000), `description`.

Available env vars in hook scripts: `GEMINI_PROJECT_DIR`, `GEMINI_PLANS_DIR`, `GEMINI_SESSION_ID`, `GEMINI_CWD`, `CLAUDE_PROJECT_DIR` (compatibility alias).

## SessionStart
Fires when a session begins (startup, resume, clear). Used to initialize resources or load context.

```json
{"event": "SessionStart", "trigger": "startup"}
```

## SessionEnd
Fires when a session ends (exit, clear). Advisory only; used to clean up or save state.

```json
{"event": "SessionEnd", "reason": "exit"}
```

## BeforeAgent
Fires after the user submits a prompt, before planning. Can block the turn or inject context; used to add context, validate prompts, or block turns.

```json
{"event": "BeforeAgent", "prompt": "Refactor the auth module to use sessions."}
```

## AfterAgent
Fires when the agent loop ends. Can force a retry or halt execution; used to review output.

```json
{"event": "AfterAgent", "result": "success", "turns": 4}
```

## BeforeModel
Fires before sending a request to the LLM. Can block the turn or mock the response; used to modify prompts, swap models, or mock responses.

```json
{"event": "BeforeModel", "model": "gemini-2.5-pro", "messages": [{"role": "user", "content": "..."}]}
```

## AfterModel
Fires after receiving the LLM response. Can block the turn or redact content; used to filter/redact responses or log interactions.

```json
{"event": "AfterModel", "response": {"role": "assistant", "content": "..."}}
```

## BeforeToolSelection
Fires before the LLM selects tools. Can filter available tools; used to optimize tool selection.

```json
{"event": "BeforeToolSelection", "availableTools": ["read_file", "write_file", "bash"]}
```

## BeforeTool
Fires before a tool executes. Can block the tool or rewrite its arguments; used to validate arguments or block dangerous operations.

```json
{"event": "BeforeTool", "tool": "bash", "arguments": {"command": "rm -rf /tmp/scratch"}}
```

## AfterTool
Fires after a tool executes. Can block or hide the result, or inject context; used to process results or run tests.

```json
{"event": "AfterTool", "tool": "write_file", "arguments": {"path": "src/index.ts"}, "result": "ok"}
```

## PreCompress
Fires before context compression. Advisory only; used to save state or notify the user.

```json
{"event": "PreCompress", "currentTokens": 180000, "targetTokens": 100000}
```

## Notification
Fires when a system notification occurs. Advisory only; used to forward to desktop alerts or logging.

```json
{"event": "Notification", "message": "Agent is waiting for input."}
```
