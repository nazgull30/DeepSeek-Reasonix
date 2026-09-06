# AGENTS.md

Reasonix — a DeepSeek-native AI coding agent for your terminal: a config- and
plugin-driven harness in a single static Go binary. This file loads into every
session's system prompt; keep it terse and durable.

## Project

- Go kernel under `internal/`; the CLI entry point is `cmd/reasonix`.
  Also `cmd/reasonix-plugin-example` (plugin skeleton) and `cmd/e2ebench`.
- Config-driven: providers, the agent, tools, and plugins are declared in
  `reasonix.toml` — nothing model-specific is hardcoded.

## Commands

```sh
make build     # -> bin/reasonix (CGO_ENABLED=0, single binary)
make cross     # -> dist/ (darwin|linux|windows × amd64|arm64)
make test      # go test ./...
make vet       # go vet ./...
make fmt       # gofmt -w .
make hooks     # git core.hooksPath -> .githooks (pre-push runs go vet)
```

- `golangci-lint` runs in CI only (not installed locally).
- CodeGraph releases are pinned via `CODEGRAPH_VERSION` in the Makefile;
  bump together with any `internal/codegraph` change.

## Code exploration

Use **codegraph** (MCP tools `codegraph_*`) as the primary way to read this
codebase — the index is kept in `.codegraph/`:

- `codegraph_search <symbol>` — find a function/type/constant by name.
- `codegraph_explore <query>` — an area's relevant symbols, source, and call
  paths in one shot.
- `codegraph_node <name>` — one symbol's source + caller/callee trail; file mode
  (`--file`) reads a file with line numbers + dependents.
- `codegraph_callers`/`codegraph_callees`/`codegraph_references`/
  `codegraph_impact` — follow the call graph or analyze blast radius.
- `codegraph_files` — the project's indexed file structure.

Prefer these over `grep`/`read` for symbol lookup and code-file reads. Use plain
`grep`/`read` only for non-indexed content: docs, configs, and gitignored files.

When the MCP tools aren't loaded (subagents, non-MCP hosts), the same surface is
the `codegraph` CLI: `codegraph query`/`callers`/`callees`, `codegraph explore`,
`codegraph files`, and `codegraph node --file=<path> <name>` (location +
dependents; `--offset`/`--limit` for a window). `codegraph sync` refreshes after
edits.

## Architecture

- `internal/control` — one transport-agnostic `control.Controller` behind every
  frontend (chat TUI, HTTP/SSE serve, Wails desktop). Add behavior here, not to
  a frontend.
- `internal/boot` — bootstraps the app and composes the system-prompt prefix.
- `internal/tool/builtin` — built-in tools self-register at compile time.
- `internal/memory` — hierarchical memory docs (`REASONIX.md`/`AGENTS.md`) and
  the auto-memory fact store.
- `internal/cli` — the command surface and slash commands.
- `internal/provider` — model providers (DeepSeek presets, any OpenAI-compatible
  endpoint).
- `internal/plugin` — external tools over stdio JSON-RPC (MCP-compatible).

## Conventions

- **Cache-first:** the system-prompt prefix (base prompt + tools + memory) must
  stay byte-stable across turns so DeepSeek's prefix cache stays warm. Never
  mutate it mid-session.
- **Import cycles:** before importing a new internal package from a non-test
  file, run `go test ./path/to/target/` to confirm the target's tests don't
  import back — `[setup failed]` means a cycle.
- **PR hygiene:** keep diffs minimal, amend review feedback (don't pile commits),
  and keep the cache-impact metadata block (Cache-impact/Cache-guard/
  System-prompt-review) for PRs touching cache-sensitive paths.
- Match the surrounding comment density and package-comment idiom when editing.

## Child Agents

`[[orchestrator.agents]]` in `reasonix.toml`:

- `web-fetcher` (groq) — web fetching; delegate content-fetching work to it
  when running under the orchestrator.

## Reasonix host checks

`verify:` directives enforced on every `complete_step` / final answer:

- `gofmt -d .`
- `go vet ./...`
- `go test ./internal/tool/builtin/ ./internal/boot/`

## Notes