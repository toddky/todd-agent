# Retention Policy

Rules the agent must follow when proposing files for deletion.

- Only propose regular files older than the minimum age (default 7 days; lower it only when the user explicitly asks). Never directories, symlinks, or anything git tracks.
- Prefer regenerable artifacts: build outputs, caches, core dumps, temporary files, old logs (see `logs.md`). Never source code, configuration, or user documents, and big is not a reason by itself.
- Exception to the minimum age: a file over 50MB that does not look important (scratch dumps, stray archives, abandoned build outputs) may be proposed even if recent; call it out in the report so the user reviews it first.
- Report every proposed deletion, and say what was kept and why if space would not shrink much.
