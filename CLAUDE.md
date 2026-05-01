# Claude Code Instructions

## Go

- **Never use `go build`** — do not compile Go code into binaries under any circumstances
- Always run Go programs with `go run <path>`
- Use `go vet <path>` for static analysis
- Use `go fix <path>` for automated fixes
- Use `go test <path>` for tests

This keeps the repo free of committed binaries and ensures all Go is run from source.

## Git

- **Never run `git push`, `git pull`, or `git fetch`** — the user controls when code moves between local and remote
- **Never run destructive git commands** (`push --force`, `reset --hard`, `branch -D`, `clean -f`, `rebase`, etc.) under any circumstances
- Only create commits when the user explicitly asks (`commit`, `commit and push`, etc.)
- For any other git operation that mutates history or shared state, ask first

## Documentation

- **Before any commit, check whether `ARCHITECTURE.md` still matches the new state.** If the change adds or removes a phase, alters the pipeline shape, changes what runs vs. what's planned, or invalidates a design decision, update `ARCHITECTURE.md` in the same commit. The doc should never describe the old state.
- Phase numbers in `ARCHITECTURE.md` are referenced from the design-decisions table and the pillars table — when renumbering or marking work complete, update every cross-reference.

## Filesystem

- **Never search outside the repo without explicit permission.** No `find`, `ls`, `stat`, `grep -r`, or any other filesystem scan of the user's home directory, Dropbox, external volumes (e.g. `/Volumes/*`), or anywhere outside the working repo. The user keeps personal data in those locations and does not want it inspected.
- For diagnostic info that lives outside the repo (dump file paths, photo originals, `~/Library/...`), ask the user to run the command and share the output.
- System paths the OS controls (`/opt/homebrew`, `/private/tmp`, `/usr/local`) are fine when relevant to a tool invocation (e.g. installing a brew formula, writing to tmp).

## Final Instruction

read README.md - read ARCHITECTURE.md - read the last 10 commits in their entirety: git log -10 --pretty=format:"%h %s%n%b%n---" --no-decorate
