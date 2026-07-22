# todd-agent Development Notes

**todd-agent** is a coding agent being built from scratch. These instructions apply to all agents working in this repository.

## Canon vs. Not Canon

This file and any `docs/` content are canon: treat them as the source of truth for how the system works today.
Plans, specs, and brainstorming notes are not canon: treat them as intent or history, not current fact.
Do not assume a plan or spec describes what is actually implemented; verify against the code.

## Operational Rules

- Prefer early returns over deep nesting.
- Write code that can be understood without referencing other files. Be explicit rather than clever.
- Use descriptive names for things (the tool registry, the agent loop), not exact file paths, since layout will move early on.
- Only add a rule to this file if it changes agent behavior. Do not describe things the model already knows from training (e.g. what `src/` is for).

