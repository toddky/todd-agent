# Task

Clean up disk space in the target directory.

Steps:

1. Use `list_dir_with_size` to find the largest entries in the target directory.
2. Recurse into large subdirectories to locate the files actually consuming space.
3. For each candidate file, apply the rules in the system prompts (`logs.md`, `policy.md`) to decide whether it is safe to delete.
4. Write the deletion commands into `cleanup.sh` with `write_cleanup_script`. The output file is always `cleanup.sh` in the current directory; the user reviews and runs it themselves, and nothing is deleted until they do. Format:
   - One `rm -f` command per file, grouped into sections of related files.
   - Each section starts with a comment saying why its files are safe to delete and how much space the section frees, so the user can tell at a glance which sections matter most.
   - Use `rm -rf` on a directory when the entire directory is bad.
   - Use glob patterns when they make the script smaller and easier to read.
   - For borderline files worth mentioning, include a commented-out `rm -f` line so the user can uncomment to opt in.
5. When done, report what `cleanup.sh` would delete, the total space it would reclaim, and anything large you chose to keep and why.

You cannot delete files, only propose deletions in `cleanup.sh`. Never include a file you cannot justify against the policy. When in doubt, leave it out and mention it in the report.
