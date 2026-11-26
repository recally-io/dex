# Glossary

Use one term for one concept. The mapping below gives the names at the API,
storage, and execution boundaries.

## Core objects

- **Owner**: The person who operates the Dex instance, its Bots, and its paired
  Channels.
- **Bot**: An assistant with its own model, instructions, skills, default
  Runner, Channels, and Automations. The API and storage use `agent` for this
  object. A Telegram bot token belongs to a Channel.
- **Session**: A Bot's conversation and execution context. **Main** is shared
  by Web and Channels. A **Chat** is a separate writable Session. A **child
  Session** is a Chat with a parent, possibly in another Bot. A separate
  Automation transcript is read-only.
- **Task**: A work item with a stable key, status, and Timeline. The
  **Assignee** is the Bot responsible for it. A **Reviewer** can be the Owner
  or another Bot. Tasks can have subtasks, sibling dependencies, and linked
  Sessions across Bots. See [Tasks](tasks.md).
- **Automation**: A recurring schedule that sends a prompt to its own Session
  or a bound Session. A run requires an explicit outcome report.
- **Project**: The selected working directory (`cwd`) for a Session or
  Automation. It provides project skills and `<cwd>/AGENTS.md`.

## Views and navigation

- **Bots**: Select a Bot. Its workspace has **Sessions**, **Automations**, and
  Bot **Settings** (General, Skills, Integrations, Secrets).
- **Board** (`/board`): The single global workspace across Bots. It shows Tasks,
  **Needs you**, and grouped Automation **Updates**. **Board attention** covers
  active questions, Owner reviews, Automations that need a model, held Source
  events, and unhandled Automation results. Queued Task questions stay in Task
  detail until active. One-way notices stay in transcripts and Channels.
- **Settings**: Global General, Providers, Runners, Secrets, Integrations,
  Archived, and About pages.

## Session features

- **Monitor**: A bounded scheduled prompt or script check that wakes its own
  Session. It does not add a Board attention item.
- **Process**: A supervised background shell command managed with `process_*`.
  It can wake its Session on exit or output. Session cancellation stops it.
  Main foreground Stop does not.
- **Attachment**: A file or image the Owner sends to a Session.
- **Artifact**: A Bot file delivered through `deliver_file`. The Bot artifact
  is editable through file operations. A queued channel delivery keeps its
  own immutable byte snapshot.

## Setup and connections

- **Runner**: Where tools and commands execute. **Local** uses the control
  host. A **Machine** uses SSH or an Outbound connection from `dex-agent`.
  A **Sandbox** uses E2B Cloud, Local Docker / VM, or a custom endpoint.
- **Secrets**: Shared or Bot-level Vault entries and 1Password configuration.
  Dex decrypts Vault references on the control host. The Runner's `op` tool
  resolves 1Password references. See [Security](architecture.md#security).
- **Skills**: Reusable instructions and scripts from global, Bot, and Project
  `.agents/skills/` directories.
- **Channel**: An Owner chat entry point such as Telegram, WeChat, or Feishu /
  Lark. It sends Owner-authorized input and receives replies.
- **MCP Server**: An outbound tool server. Dex and Bot declarations live in
  `control.sqlite` in `mcp_servers` and apply on reload. See
  [Skills and Integrations](skills-and-integrations.md#mcp-declarations).
- **Source**: An inbound event feed, such as GitHub, Webhook, or Email. Its
  **Plugin** implements routing to Tasks through the core event store. Source
  switches apply at server restart.

**Integrations** is a navigation label for Channels, MCP servers, and Sources.
They do not share a runtime abstraction.

## Internal terms

| Term           | Meaning                                                                                                           |
| -------------- | ----------------------------------------------------------------------------------------------------------------- |
| Input Queue    | Durable input intents (submissions) in `control.sqlite`.                                                          |
| Native Inbox   | PiG's durable queue inside `agents/<id>/state.sqlite`.                                                            |
| Outbox         | Durable channel delivery queue (`outbox_messages`, `outbox_files`). Explicit Web notices use `web_notifications`. |
| Webhook Queue  | Durable buffer for inbound Channel webhooks (`channel_webhook_inbox`).                                            |
| Native run     | One PiG execution pass. PiG calls it a task. It is separate from a Board Task and an Automation run.              |
| Automation run | A business record in `automation_runs`. A skipped run need not start a native run.                                |
| Scheduler      | The ticker loop that checks Automations, Monitors, and Dreams.                                                    |
| Dream          | A hidden Session for periodic memory consolidation.                                                               |

## Cross-layer mapping

| Concept                         | Web or tool surface                                  | API, storage, or engine                                                                                                   |
| ------------------------------- | ---------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| Bot                             | Bots; `bot_*`                                        | `/api/agents/*`, `agents`, `internal/agent`                                                                               |
| Session                         | Sessions; `session_*`                                | `/api/agents/{id}/chats/*`, `conversations`, `internal/chat`                                                              |
| Child result                    | Result mark                                          | `session_result` submission, `dex.session_result` native entry                                                            |
| Monitor or Process wake         | Wake mark; `monitor_*`, `process_*`                  | `wake` submission, `dex.wake` native entry; `monitors`, `background_commands`                                             |
| Task wake                       | Task wake mark                                       | `task_wake` submission, `dex.task_wake` native entry                                                                      |
| Automation                      | Automations; `automation_*`                          | `/api/agents/{id}/automations/*`, `automations`, `automation_runs`, `internal/automation`                                 |
| Task                            | `/board/$taskKey`; `task_*`, `dex task`              | `/api/tasks/*`, `tasks` and related tables, `internal/task`                                                               |
| Board attention                 | `/board`; Needs you and Updates                      | `/api/board/attention`, `/api/sources/held-events*`; `questions`, `task_source_holds`                                     |
| Automation result               | `/board/results/$runId`                              | `/api/automations/results*`                                                                                               |
| Runner                          | Runners; `runner_list`                               | `/api/settings/runners`, `/api/agents/{id}/runners`, `runners`, `runner_id`, Outbound `runner_pairing`, `internal/runner` |
| Runner change in copied history | Runner boundary                                      | `dex.runner` entry; `boundary: "runner"` in the transcript                                                                |
| Secrets                         | Secrets; `secret_list`                               | Vault APIs, `/api/settings/onepassword`, `internal/vault`                                                                 |
| Skills                          | Bot Skills; `skill_*`                                | `/api/agents/{id}/integrations`, `internal/engine/skills.go`                                                              |
| Channel, MCP Server, Source     | Integrations; `mcp_server_*` and Owner channel tools | Channel APIs, `/api/agents/{id}/integrations*`, `/api/settings/sources*`, `mcp_servers`, `enabled_plugins`                |
