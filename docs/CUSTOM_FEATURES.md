# Custom Features — nazgull30/DeepSeek-Reasonix

This document catalogs all features and changes in this fork relative to upstream
[esengine/DeepSeek-Reasonix](https://github.com/esengine/DeepSeek-Reasonix).

---

## 1. In-Process Multi-Agent Orchestrator

**Package:** `internal/orchestrator/`

A full in-process orchestrator that lets the main agent spawn and communicate with
managed child agents, each running with its own model, tools, system prompt, and
MCP scope — all within the same process.

### Architecture

- **`Orchestrator`** (`orchestrator.go`) — Manages a map of `ManagedAgent` instances,
  wired into the main controller. Provides `AddAgent`, `Agent(name)`, `Agents()`,
  `AgentNames()`, and session directory management.

- **`ManagedAgent`** (`agent.go`) — Wraps a `*control.Controller` with status
  tracking (`ready → running → done`), last-task/result/error state, an `Inbox`
  for future async messaging, and a `SinkMultiplexer` for event forwarding.

- **`SinkMultiplexer`** (`sink.go`) — Forwards child agent events to the parent's
  event sink. Supports `Verbose` mode (show full child transcript) and standard
  mode (show only start/done notices). Filters `Text` events in non-verbose mode
  to save tokens.

- **`Inbox`** (`message.go`) — Future async message passing between agents.

### Slash Commands

All commands support tab-completion (`/agent_<TAB>`) and help text:

| Command | Description |
|---------|-------------|
| `/agent_spawn <name> <task>` | Delegate a task to a named child agent; blocks until complete |
| `/agent_send <name> <message>` | Send a follow-up message to a previously spawned agent |
| `/agent_stats [name]` | Show tokens, cost, cache hit/miss stats for one or all agents |
| `/agent_clear [name]` | Reset a child agent's session (safe on shutdown too) |
| `/context <name>` | Token context breakdown for a child agent |

### Tool Interface

- **`agent_spawn` tool** — LLM-accessible tool (not just slash command) for
  delegating sub-tasks programmatically. Accepts `name` + `task`.

### Configuration (`reasonix.toml`)

```toml
[agent]
  [agent.orchestrator.children]
    [agent.orchestrator.children.architect]
      model = "deepseek-reasoner"
      system_prompt = "You are a solution architect..."
      verbose = true
      tools = ["bash", "read_file", "glob", "grep"]
      skip_codegraph = true
```

Key fields per child:
- `model` — Provider/model override (defaults to main agent)
- `system_prompt` — Custom system prompt
- `verbose` — Show full transcript in parent view (default: false)
- `tools` — Optional tool allowlist
- `skip_codegraph` — Skip CodeGraph install for this child (avoids N concurrent downloads)

### Automatic Session Persistence

Child agent sessions are auto-persisted to disk at `~/.reasonix/sessions/` via
the orchestrator's `sessionDir`. Each child creates its own checkpoint store and
session file. The session path is set before `os.Stat` so first-run agents can
immediately snapshot.

---

## 2. CodeGraph — Bundled Integration

**Package:** `internal/codegraph/`
**CLI:** `internal/cli/codegraph.go`

Upstream v1.10.0 removed the bundled CodeGraph code-intelligence server. This
fork keeps it bundled and adds management commands.

### CLI Commands (`reasonix codegraph`)

| Subcommand | Description |
|------------|-------------|
| `install` | Download and install the CodeGraph binary |
| `sync` | Forward command to the CodeGraph sync subprocess |
| `index` | Forward command to the CodeGraph index subprocess |
| `status` | Show CodeGraph status (enabled, version, path, cache) |
| `help` | Print usage |

### Key Changes

| Change | Detail |
|--------|--------|
| **Periodic sync** | Replaced file watcher with periodic sync to reduce resource usage |
| **Sync guarded by Initialized** | Sync only runs after CodeGraph is fully initialized |
| **Updated checksums** | Checksums bumped for CodeGraph v1.0.1 |
| **Download URL** | Mirror URL changed to `dl.reasonix.io`, download timeout increased to 60s |
| **SkipCodegraph config** | Option to skip CodeGraph install per child agent |
| **Tool registration** | CodeGraph tools registered for main agent |

### Configuration

```toml
[codegraph]
enabled = true
auto_install = true
path = ""
```

### Updating the Custom Bundle

This fork bundles a custom version of codegraph with GDScript support built from
an external source repo.

| Detail | Value |
|--------|-------|
| **Source repo** | `/Users/nazgul/Documents/Projects/Sources/Misc/codegraph` |
| **Bundle root** | `codegraph/` (repo root) |
| **Launcher** | `codegraph/bin/codegraph` — resolves symlinks, runs `node --liftoff-only "$DIR/lib/dist/bin/codegraph.js"` |
| **Built output** | `codegraph/lib/` — gitignored, contains `dist/`, `node_modules/`, `package.json` |

To update the bundle after making changes in the source repo:

```bash
# 1. Build the source project
cd /Users/nazgul/Documents/Projects/Sources/Misc/codegraph
npm ci
npm run build

# 2. Replace the bundle in this repo
cd /Users/nazgul/Documents/Projects/Sources/Misc/DeepSeek-Reasonix
rm -rf codegraph/lib
cp -r /Users/nazgul/Documents/Projects/Sources/Misc/codegraph/dist   codegraph/lib/dist
cp -r /Users/nazgul/Documents/Projects/Sources/Misc/codegraph/node_modules codegraph/lib/node_modules
cp    /Users/nazgul/Documents/Projects/Sources/Misc/codegraph/package.json     codegraph/lib/package.json
cp    /Users/nazgul/Documents/Projects/Sources/Misc/codegraph/package-lock.json codegraph/lib/package-lock.json
```

The launcher resolves `$DIR` relative to its own symlink-resolved location, so
the `codegraph/lib/dist/bin/codegraph.js` path is always correct regardless of
how the launcher is invoked.

---

## 3. Per-Agent MCP Scoping

**Files:** `internal/boot/boot.go`, `internal/plugin/plugin.go`,
`internal/plugin/lazy.go`, `internal/config/config.go`, `internal/boot/boot.go`

MCP plugin scopes can be restricted to specific agents, so a child agent only sees
the tools it needs.

### `PluginEntry.Agents` Field

Added an `Agents` field to the `PluginEntry` config struct:

```toml
[[plugins]]
name = "db-server"
command = "node"
args = ["db-mcp.js"]
agents = ["architect", "db-agent"]    # only these agents see this MCP's tools
```

- If `agents` is empty/omitted, the plugin is available to **all** agents (default).
- If set, only named agents (and the main agent if it matches) get the tools.
- Boot wiring filters tool registration by agent name.

### Plugin Lazy Resolution

`internal/plugin/lazy.go` updated to support per-agent scoping in lazy plugin
initialization, ensuring scoped servers are only started for their target agents.

---

## 4. Context Breakdown (`/context`)

**Package:** `internal/agent/context.go`, `internal/agent/context_test.go`

Provides a detailed token breakdown of the session context, helping users
understand exactly what is consuming their context window.

### `ContextBreakdown` structure

| Field | Description |
|-------|-------------|
| `SystemPromptTokens` | Token count of system prompt |
| `ToolSchemaTokens` | Token count of all tool schemas |
| `PerToolSchemas` | Per-tool breakdown of schema costs |
| `ConversationTokens` | Token count of all non-system messages |
| `Turns` | Per-turn breakdown (turn #, prompt preview, tokens, file changes) |
| `TotalEstimated` | Sum of all above (local approximation) |
| `Usage` | Real API-reported usage (input/prompt/completion/reasoning tokens) |
| `CacheHitPct` | Cache hit percentage from the API |
| `Window` | Current context window size |
| `CompactPct` | Compaction threshold percentage |
| `Verbose` | Render raw integers instead of short-form |

### Per-Turn Breakdown

Uses checkpoint boundaries to attribute tokens to individual user turns, showing:
- Turn number
- User prompt text (truncated)
- Estimated tokens
- Number of files changed in that turn

### Context Window Gauge

Visually shows how full the context window is with a percentage indicator,
using per-turn prompt tokens (not cumulative totals) for the window calculation.

### Support for Child Agents

`/context <agent_name>` shows the context breakdown for a specific managed child agent.

---

## 5. Session Token/Cost Tracking (`/stats`)

**Files:** `internal/agent/branch.go`, `internal/cli/chat_tui.go`,
`internal/agent/agent.go`

### Session totals persisted to BranchMeta

Token and cost session totals are written to a `BranchMeta` sidecar file alongside
session history, so `/stats` survives restarts.

### `/stats` command

Shows in the TUI:
- Total input tokens
- Total output tokens
- Total cost (in USD)
- Per-turn breakdown

### `/agent_stats` command

Same as `/stats` but for child agents. Includes:
- Cache hit/miss indicators
- Reasoning token counts
- Token type breakdown (same as main `/stats`)

---

## 6. Built-in MCP Time Server

**Package:** `internal/builtinmcp/`

A time server bundled with the binary that provides time/date information to the
agent via MCP protocol, without requiring any external dependencies.

### Features

| Tool | Description |
|------|-------------|
| `get_current_time` | Returns current date, time, timezone, UNIX timestamp, and weekday |
| `get_current_time_by_timezone` | Same as above for a specified IANA timezone (e.g., `America/New_York`) |
| `get_current_date` | Returns just the formatted date + weekday |

### Registration

Registered at compile time via `init()` in `internal/builtinmcp/builtinmcp.go`,
alongside external plugin MCP servers.

---

## 7. Session Inspection Package (`internal/inspect/`)

**Package:** `internal/inspect/inspect.go`

A read-only projection layer that serializes the agent's capabilities into
JSON-serializable structs for frontend rendering.

### `Capabilities()` Snapshot

Returns a complete snapshot of:
- **Default model** — currently selected model name
- **Providers** — list of configured providers with key presence indicator
- **Tools** — built-in + MCP tools (name, description, read-only, previewable)
- **Servers** — connected MCP server details
- **Prompts** — MCP server prompt definitions
- **Resources** — MCP server resource URIs
- **Commands** — loaded slash commands

Used by the desktop settings panel and CLI `/mcp` listing.

---

## 8. Clipboard Improvements

**Files:** `internal/clipboard/clipboard.go`, `internal/clipboard/clipboard_darwin.go`,
`internal/clipboard/clipboard_linux.go`, `internal/clipboard/clipboard_windows.go`

### Change

Replaced the `atotto/clipboard` Go library with direct platform commands:
- **macOS:** `pbcopy` / `pbpaste`
- **Linux:** `xclip` / `xsel`
- **Windows:** `powershell` commands

### Benefits

- Removes CGo dependency from clipboard operations
- 2-second timeout on clipboard read/write to prevent hanging
- Better error messages on failure

### Additional CLI Changes

- **Mouse-toggle support** on macOS (right-click paste)
- **Startup tip** added about `Ctrl+C` copy and mouse-toggle on macOS
- Paste event reordering for better UX

---

## 9. Token Economy Mode

**File:** `internal/boot/token_profile.go`

A tool-surface reduction mode that trims the agent's visible tool set to core
built-ins only, reducing prompt token consumption.

### Modes

| Mode | Description |
|------|-------------|
| `full` (default) | All tools available |
| `economy` / `eco` | Core built-ins only: bash, read, write, edit, glob, grep, ls, etc. |

### `connect_tool_source` Tool

In economy mode, exposes a `connect_tool_source` tool that lets the agent
on-demand enable additional tool sources:
- `skills` — Enable skill tools
- `mcp` — Connect a specific MCP server by name
- `lsp` — Enable LSP/code intelligence tools
- `web_fetch` — Enable web fetching
- `install_source` — Enable plugin/skill installer tools
- `task` — Enable sub-agent delegation

### Configuration

```toml
[agent]
token_mode = "economy"
```

Or via `/effort` mode selection in the TUI.

---

## 10. Diff Viewer Enhancements

**File:** `internal/cli/diffview.go`

Enhanced side-by-side diff rendering with fold indicators for collapsed
unchanged regions, improving readability of large diffs in the TUI.

---

## 11. Config Changes

### DeepSeek Default Pricing in USD

**File:** `internal/config/config.go`, `internal/config/render.go`

Changed the hardcoded DeepSeek pricing from ¥ (CNY) to USD to match the
international billing model:
- deepseek-chat: $0.27/M input, $1.10/M output
- deepseek-reasoner: $0.55/M input, $2.18/M output (with reasoning)

### Codegraph & BuiltInMCP Config Types

**File:** `internal/config/config.go`

Added `CodegraphConfig` and `BuiltInMCPConfig` types to the configuration system
for the bundled CodeGraph and built-in MCP features.

### Orchestrator Agent Config

**File:** `internal/config/config.go`

Added `OrchestratorAgentEntry` configuration type supporting:
- `model` — per-agent model override
- `system_prompt` — custom system prompt per agent
- `verbose` — transcript visibility
- `tools` — tool allowlist
- `skip_codegraph` — skip codegraph for this agent

---

## 12. Tests Added

| Test File | What it Covers |
|-----------|---------------|
| `internal/orchestrator/orchestrator_test.go` | Orchestrator lifecycle, agent management |
| `internal/orchestrator/agent_test.go` | Managed agent creation, status transitions |
| `internal/orchestrator/session_test.go` | Child session creation and persistence |
| `internal/orchestrator/sink_test.go` | SinkMultiplexer event forwarding, verbose mode |
| `internal/orchestrator/tools_test.go` | agent_spawn and agent_send tool execution |
| `internal/orchestrator/message_test.go` | Inbox message passing |
| `internal/agent/context_test.go` | Context breakdown computation |
| `internal/codegraph/codegraph_test.go` | CodeGraph lifecycle, tool registration |
| `internal/codegraph/install_test.go` | CodeGraph binary download and installation |
| `internal/codegraph/symlink_escape_test.go` | Symlink escape prevention |
| `internal/codegraph/steer_test.go` | CodeGraph steer/guard behavior |
| `internal/codegraph/e2e_test.go` | End-to-end CodeGraph integration |
| `internal/builtinmcp/time_server_test.go` | Time MCP server tools |
| `internal/builtinmcp/builtinmcp_test.go` | Built-in MCP server registration |
| `internal/clipboard/clipboard_test.go` | Clipboard read/write/probe |
| `internal/inspect/inspect_test.go` | Capabilities snapshot serialization |

---

## 13. Other Fixes & Tweaks

| Commit | Description |
|--------|-------------|
| `f9698914` | Use `resolveCLISessionDir` for orchestrator session paths |
| `bd4d889a` | Guard snapshot against nil session to prevent panic on shutdown |
| `adfb8c51` | Stop forwarding Text events from child agents to save tokens (non-verbose) |
| `6fbbcecf` | Remove Text forwarding duplication, wrap delegation results with completion marker |
| `94716d44` | Display child agent response in transcript for `/agent_spawn` and `/agent_send` |
| `e90258cb` | Resolve merge conflicts in tests and i18n |
| `e157499f` | Remove stray closing brace in `maybeColdResumePrune` |
| `055a4a1a` | Rename agent status `idle` → `ready` for clarity |
| `66567d2d` | Use per-turn prompt tokens for window gauge, break cumulative totals by input/output |
| `cdeccffa` | Register CodeGraph tools for the main agent |
| `f3b01996` | Add startup tip about Ctrl+C copy and mouse-toggle on macOS |
