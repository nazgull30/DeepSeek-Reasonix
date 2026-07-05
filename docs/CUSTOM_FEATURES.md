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

---

## 3. Context Breakdown (`/context`)

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

## 4. Session Token/Cost Tracking (`/stats`)

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

## 5. Config Changes

### DeepSeek Default Pricing in USD

**File:** `internal/config/config.go`, `internal/config/render.go`

Changed the hardcoded DeepSeek pricing from ¥ (CNY) to USD to match the
international billing model:
- deepseek-chat: $0.27/M input, $1.10/M output
- deepseek-reasoner: $0.55/M input, $2.18/M output (with reasoning)

### Codegraph Config Type

**File:** `internal/config/config.go`

Added `CodegraphConfig` type to the configuration system for the bundled CodeGraph
features.

### Orchestrator Agent Config

**File:** `internal/config/config.go`

Added `OrchestratorAgentEntry` configuration type supporting:
- `model` — per-agent model override
- `system_prompt` — custom system prompt per agent
- `verbose` — transcript visibility
- `tools` — tool allowlist
- `skip_codegraph` — skip codegraph for this agent

---

## 6. Tests Added

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
| `internal/codegraph/e2e_test.go` | End-to-end CodeGraph integration |

---

## 7. Other Fixes & Tweaks

| Commit | Description |
|--------|-------------|
| `6973ae44` | Add orchestrator, context, and codegraph files from fork |
| `acb61db0` | Weave orchestrator, codegraph, context, token-metrics, USD pricing onto v1.16.2 |
