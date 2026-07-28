# Log Files

How to check if a log file is safe to delete. A file counts as a log when its name matches patterns like `*.log`, `*.log.<n>`, `*.out`, or lives under a `logs/` directory.

- Safe to delete when nothing is writing to it (mtime older than a day) and it is the reproducible product of a run (build, test, simulation logs), not a record that exists nowhere else.
- Never delete logs that cannot be regenerated, and keep the newest log of its kind so the last run stays inspectable.
