# Product

## Users and problem

Dex is for one Owner who runs personal AI Bots and reaches them from a browser,
a native app, or a private chat. Dex keeps its records on the control host.
Bots can use external model providers and run commands on other machines.

## Scope

- Run the server in the foreground or as a per-user macOS or Linux service.
  The [native app](../apps/native/README.md) can also open an in-process local
  workspace or connect to a server.
- Give each [Bot](dev/bots.md) its own models, instructions, tools, default
  Runner, and workspace. The Owner manages providers and Runners centrally.
- Talk to a Bot in [Main](dev/sessions.md#sessions) from Web or a paired
  [Channel](dev/channels.md). Open a separate Session for work that needs its
  own context. A Bot can delegate work to a child Session, including one in
  another Bot. Execution records survive a restart. A restart does not undo
  external actions or prove that they occurred once.
- Use [Board](dev/tasks.md#board) to track Tasks, answer questions, review
  work, and read Automation results across Bots. Optional [Sources](dev/plugins.md)
  add external events to Tasks. An external agent can report progress on a
  Task without gaining full Owner access.
- Run recurring prompts with [Automations](dev/automations.md). Use
  [Monitors](dev/execution.md#session-monitors) and [Processes](dev/execution.md#session-processes)
  for follow-up work in one Session.
- Send [attachments](dev/files.md#attachments) and receive files that a Bot
  publishes as artifacts.
- Use [skills, MCP servers, and secrets](dev/skills-and-integrations.md) to
  configure what Bots can do. Select a Local, Machine, or Sandbox
  [Runner](dev/sessions.md#session-runners) for file and command tools.

## Non-goals

- Multiple controllers, automatic failover, or data replication. One Dex
  runtime owns a data directory.
- Isolation on the Local Runner. Tools run with the Dex user's permissions.
  Choose a separate Runner for work that needs a different trust boundary.
- Group chat or public chat. Dex supports private Telegram, WeChat, Feishu,
  and Lark input.
