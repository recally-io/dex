# 0016: Cross-bot child sessions and bot management tools

Status: Accepted (2026-11-11). Builds on [ADR 0015](0015-child-sessions.md).
Changes the bot isolation rule in `docs/product.md` ("These tools use the
current bot; they cannot manage another bot's routines") for the tools below.

## Context

Each bot has its own instructions, model, memory section, skills, MCP servers,
`.env`, and workspace. A bot cannot reach another bot: its tools act on the
current bot, the API returns 404 for another bot's resources, and `DEX_TOKEN`
is not in the tool environment. The only shared state is the memory repository.

The owner wants one bot to give work to another bot, for example Main gives a
coding change to the Coder bot, and wants bots to read each other's sessions.
The owner also wants to manage bots from a conversation.

All bots share `control.sqlite`. Each conversation has an `agent_id`, and the
engine opens a Harness per agent on demand. ADR 0015 delivers a child result by
writing a submission row to the parent conversation. That write does not
depend on the agent of the child.

dex has no bot deletion today, in the API or the web UI.

## Decision

### Child sessions in another bot

`session_new` gets `bot` (an ID or a name; empty means the current bot). The
child conversation belongs to the target bot, and its `parent_id` names the
caller's conversation. The child runs with the target bot's instructions,
model defaults, memory section, skills, MCP servers, `.env`, and workspace.
Its result goes to the parent as in ADR 0015.

- The parent can operate the child with `session_send`, `session_stop`, and
  `session_fork`, whatever the child's bot is. Ownership is
  `child.parent_id = caller conversation`.
- A child still cannot create children, so bots cannot delegate in a loop.
- This gives the caller the target bot's tools and credentials. dex has one
  owner, and channel input comes only from paired or allowed users, so this is
  accepted.

### Reading and search across bots

- `session_read` reads any session of any bot, except dreams.
- `session_search` gets `bot`: empty searches the current bot, `all` searches
  every bot, and an ID or a name searches that bot.
- `session_list` gets the same `bot` argument, with the same default.
- Each result names its bot.

### Bot tools

| Tool         | Arguments                                                      | Operation                                                                                   |
| ------------ | -------------------------------------------------------------- | ------------------------------------------------------------------------------------------- |
| `bot_list`   | none                                                           | ID, name, model, first line of the instructions, running session count.                     |
| `bot_get`    | `bot`                                                          | Name, model, thinking level, instructions, skill names, MCP server names. No `.env` values. |
| `bot_create` | `name`, `instructions`, `provider`, `modelId`, `thinkingLevel` | The same as the web create dialog and its checks.                                           |
| `bot_update` | `bot`, then any of the `bot_create` fields                     | The same as the bot settings page and its checks. Fields that are not given do not change.  |
| `bot_delete` | `bot`, `reason`                                                | Asks the owner. The bot is archived only after the owner approves. See below.               |

Main, Chat, and automation conversations get `bot_list` and `bot_get`.
`bot_create`, `bot_update`, and `bot_delete` need a user-input turn in Main or a
Chat, as the routine tools do. Child sessions, automations, and dreams cannot
use them.

The tools do not set channels, skills, MCP servers, or `.env`. A new bot gets
the home and dex extension levels.

### Delete needs owner approval

The dex tools cannot delete a bot. Control archives it after the owner approves.
The approval stops model mistakes and injected instructions that act through the
dex tools. It is not a boundary against a model that uses bash. Tools run as the
same OS user as dex, so bash can call the local API or write the database, as it
can already change any bot setting or delete data. Archive can be restored, so
dex accepts this risk. The API has no archive route: restore is the only archive
route. Isolation of tools is a separate decision.

1. `bot_delete` writes a question with the ask_user card: "Archive bot
   `<name>`? Reason: `<reason>`", options "Archive `<name>`" and "Cancel". The
   question row also stores the action (`archive`, bot ID). The tool returns
   `pending_approval`, and the turn ends, as with ask_user.
2. The owner answers in the web card or in the channel, as for any question.
3. Before the answer reaches the model, control checks it. Only an answer from
   a user origin whose text is exactly the approve option runs the action. Any
   other answer cancels it. Control writes the outcome ("archived" or "not
   approved") into the input that the model sees.

Rules:

- The default bot cannot be deleted.
- A bot cannot delete itself.
- A bot with a running session cannot be deleted. The tool returns an error,
  and asks the caller to stop the sessions first.

### Delete is archive

Archive hides the bot from the sidebar and from `bot_list`. It stops its
channels and pauses its automations. It keeps conversations, memory, workspace,
and `state.sqlite`. The owner can restore it on the web page that lists
archived bots. Hard deletion of data is not in this ADR.

## Consequences

- One bot can give work to the bot that has the right skills and credentials,
  and get the result back in the same conversation.
- Bot isolation now has these exceptions. A reviewer must check each new
  cross-bot path against the ownership rule above.
- A settlement in bot B writes to a conversation of bot A. The tests must show
  that the delivery locks of A and B cannot deadlock.
- Archive needs an `agents.archived_at` column, filters in every agent list,
  and a restore path in the web UI.

## Alternatives

- Let bots call the dex HTTP API with a token. This exposes every API to tools
  and has no ownership check.
- Use the memory repository as a mailbox. It is async, it has no result
  delivery, and it mixes messages with facts.
- Hard delete. It cannot be undone, and it must remove data in several stores.
