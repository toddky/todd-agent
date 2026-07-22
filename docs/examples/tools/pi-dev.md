# pi.dev (`pi`) Tool Calls

## read
Read the contents of a file. Supports text files and images (jpg, png, gif, webp, bmp). For text files, output is truncated to a max line/byte limit; use offset/limit for large files.

```json
{"path": "src/index.ts", "offset": 1, "limit": 200}
```

## write
Write content to a file, creating it if it doesn't exist.

```json
{"path": "docs/notes.md", "content": "# Notes\n\nDraft content."}
```

## edit
Edit a single file using exact text replacement. Every `edits[].oldText` must match a unique, non-overlapping region of the original file.

```json
{
  "path": "src/index.ts",
  "edits": [
    {"oldText": "const timeout = 30;", "newText": "const timeout = 60;"}
  ]
}
```

## bash
Execute a bash command in the current working directory. Returns stdout and stderr. Optionally provide a timeout in seconds.

```json
{"command": "npm run build", "timeout": 300}
```

## grep
Search file contents for a pattern (regex or literal string). Returns matching lines with file paths and line numbers. Respects .gitignore.

```json
{"pattern": "createReadToolDefinition", "glob": "*.ts", "context": 2}
```

## find
Search for files by glob pattern. Returns matching file paths relative to the search directory. Respects .gitignore.

```json
{"pattern": "**/*.spec.ts", "path": "packages/agent"}
```

## ls
List directory contents. Returns entries sorted alphabetically, with `/` suffix for directories. Includes dotfiles.

```json
{"path": "packages/coding-agent/src/core/tools", "limit": 100}
```
