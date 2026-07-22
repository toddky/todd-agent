# native-cli-ai (`nca`) Tool Calls

## read_file
Reads a file's full contents as a UTF-8 string.

```json
{"path": "src/main.rs"}
```

## search_code
Shells out to ripgrep and returns structured JSON matches (path, line/column, matched text, before/after context).

```json
{"pattern": "fn execute", "glob": "*.rs"}
```

## list_directory
Lists file and directory entries under a path (directories suffixed with `/`).

```json
{"path": "crates/core/src/tools"}
```

## git_status
Runs `git status --short --branch` in the workspace.

```json
{}
```

## git_diff
Runs `git diff` (or `git diff --cached` when staged) with color disabled.

```json
{"staged": true}
```

## web_search
Searches DuckDuckGo's HTML endpoint and returns titles, URLs, and snippets.

```json
{"query": "rust async trait object safety", "limit": 5}
```

## fetch_url
Fetches a URL and normalizes HTML or plain-text content to whitespace-collapsed text.

```json
{"url": "https://nca-cli.com/docs"}
```

## query_symbols
Looks up likely Rust symbol definitions by literal name via a local code-intel index.

```json
{"query": "ToolRegistry", "glob": "*.rs"}
```

## write_file
Creates or overwrites a file, creating parent directories as needed.

```json
{"path": "docs/notes.md", "content": "# Notes\n\nDraft content."}
```

## create_directory
Creates a directory (and any missing parents).

```json
{"path": "docs/examples/tools"}
```

## apply_patch
Applies one or more exact string replacements to a file in a single call.

```json
{
  "path": "src/lib.rs",
  "edits": [
    {"old_text": "fn old_name()", "new_text": "fn new_name()"}
  ]
}
```

## edit_file
Replaces a specific string in an existing file.

```json
{"path": "src/lib.rs", "old_text": "max_retries: 3", "new_text": "max_retries: 5"}
```

## replace_match
Replaces a match at an exact line/column coordinate, verifying the expected text is present before editing.

```json
{"path": "src/lib.rs", "line": 42, "column": 9, "old_text": "unwrap()", "new_text": "expect(\"config must exist\")"}
```

## rename_path
Renames a file or directory within the workspace.

```json
{"from": "src/old_module.rs", "to": "src/new_module.rs"}
```

## move_path
Moves a file or directory within the workspace.

```json
{"from": "drafts/plan.md", "to": "docs/plan.md"}
```

## copy_path
Copies a file within the workspace, creating the destination directory if needed.

```json
{"from": "templates/default.toml", "to": "config/local.toml"}
```

## delete_path
Deletes a file or directory.

```json
{"path": "tmp/scratch", "recursive": true}
```

## run_validation
Runs a build, test, or lint command in the workspace, restricted to an allowlist of safe commands.

```json
{"command": "cargo test --workspace", "timeout_secs": 180}
```

## execute_bash
Runs an arbitrary shell command in the workspace with a timeout.

```json
{"command": "cargo build --release", "timeout_secs": 300}
```

## ask_question
Asks the user a structured question with multiple choices and an optional free-text answer.

```json
{
  "question": "Which provider should I configure?",
  "choices": ["MiniMax", "OpenAI", "Anthropic"],
  "allow_free_text": true
}
```

## update_todos
Replaces the session's todo list atomically.

```json
{
  "todos": [
    {"content": "Read agent.rs", "status": "completed"},
    {"content": "Fix retry logic", "status": "in_progress"}
  ]
}
```

## invoke_skill
Loads a skill's full instructions by name when a task matches an available skill definition.

```json
{"name": "git-commit-message"}
```

## spawn_subagent
Spawns a sub-agent as a separate session (with its own git worktree) to handle a specific delegated task.

```json
{"task": "Audit crates/core/src/tools for missing input validation", "reason": "isolated exploration"}
```

## MCP tools (`mcp__<server>__<tool>`)
Bridges external MCP servers into the tool registry, e.g. calling a configured `github` server's `search_issues` tool.

```json
{"tool": "mcp__github__search_issues", "input": {"query": "is:open label:bug"}}
```
