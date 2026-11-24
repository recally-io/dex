# Channels

This document describes the channel adapters and how they share delivery.

## Isolated Telegram resource runtime

`engine.Ref.Scope` selects native resource ownership in addition to the real bot
and native conversation ID. Empty scope keeps private execution. A canonical
Telegram bot/chat/topic scope selects separate execution storage under
`agents/<id>/telegram-groups/<scope>/state.sqlite`. Its `resources/` directory
holds explicitly published group files and operation-owned notes. Native IDs
are local to each store. Forks, cancellation, recovery and activity keys retain scope.

The group constructor bypasses private memory, profile/instructions, AGENTS,
project files, skills, environment capture and MCP loading. Its registry contains
only narrow group resource, note and file-delivery tools. A before-tool hook enforces a fixed
execution allowlist for direct, nested, deferred and task-owned-child calls.
Filesystem handlers use a scope-rooted handle and reject traversal and symlinks.
Group input configuration cannot add private extensions or a project; settlement
does not commit private memory. Group attachment text contains relative resource
paths, not the host's private directory path.

Control persists the execution scope and immutable bot/chat/topic/session-sender
binding on the business conversation. Each source stores its actual sender. Ordinary
groups separate sender sessions inside one audience; topics share their own session.
Native lookup, cancellation, recovery, live snapshots and history retain the stored
scope. A group source cannot acquire private owner authority through an equal native
ID. Private session/bot/Routine handlers and bot-wide artifact sharing deny scoped callers.

A resource scope is not an actor grant. Admission and every group provider request
and tool execution check the current Telegram account and sender allowlist against
the persisted source. Revocation also blocks public delivery. PiG reports ordinary
generation hook errors and continues, so a denied request uses its native task abort
mark to cancel that invocation before provider dispatch. Group inputs stay outside
private dream consolidation.

Group `requireMention` defaults to false, as the selected Hermes trigger default.
A current paired sender can trigger work without a mention. The channel settings can
require a mention or reply; that trigger switch never grants identity or resource access.
Only a current paired sender can trigger work or downloads. Ordinary groups separate sender sessions;
topics share their topic session, including forum General without a thread ID.
`/stop`, `/clear` and `/new` use the parsed session. Saved commands bind the actual
actor and peer, and cannot acquire Main's scope after restore.

Group uploads live in their audience's `resources/uploads/`, not the private workspace.
Album collection and cleanup retain that ownership. Scope-local stop/clear discards only
its pending albums; private Main cannot discard group albums. `group_resource` reads
published UTF-8 text up to 64 KiB or common image bytes up to 20 MiB. `group_deliver`
queues only a rooted audience file in the existing outbox. It never copies to bot-wide
artifacts. Topic replies, files and media groups set the exact message thread.

`group_session` starts, lists, reads, sends to or stops only the current conversation's
direct children in the same bot and resource audience. A child retains the persisted
scope and exact initiating source. Current personal authorization follows that source
through native admission, provider requests, tools and restore. Children cannot delegate,
select another bot, choose a project or override the model. Private session handlers stay
unavailable. Children return published resource paths; the parent queues Telegram delivery.

`group_question` asks one public question with two to four options and ends the turn.
Only the initiating paired sender can answer in the same conversation. It cannot approve
owner actions. Children report decisions to their parent instead of asking directly.
Private `ask_user` and owner-action Question handlers stay unavailable in groups.

Telegram `/menu`, `/status`, `/settings`, `/sessions`, `/question` and `/workflows` use the
current parsed conversation. Session lists contain only its direct same-audience children.
Saved workflow controls reuse those children and their stored first prompt. `/run <child ID>
[message]` queues another parent-owned child turn. It retains the original Telegram source,
current personal grant and resource scope. It does not invoke private Routines or route
group work through Main. Only the initiating Telegram sender can resume the child.

Inline buttons carry a 96-bit HMAC reference signed with the current bot token. An immutable
server row binds the actor, account, chat, topic, confirmed message, conversation, execution
scope, native ID, context-reset receipt, object, action and a 15-minute expiry. The claim
transaction checks current authority and consumes the nonce with its durable command intent.
Command processing and effect transactions use one read-only validity check
(`chat.ValidateTelegramControl`); effects repeat it under the SQLite writer. It covers the
current grant, expiry, actor, message, object and context revision. A definite 400 or 403
rejection of the final `answerCallbackQuery` does not hold the polling offset; network, 5xx
and 429 errors retry the same update, and the consumed nonce cannot queue a second intent.
Clear does not change the native conversation ID, so callbacks also bind the latest
`context_reset` receipt. Replays cannot queue another action. A matching actor's rejected grant cancels its
button permanently. Command parts and button labels stay frozen across delivery retries.
Message delivery stays at least once; only the bound confirmed remote message accepts its
buttons. A stale Question button cannot become ordinary model input.

Group speech does not borrow private consent or provider settings. Original audio
stays attached. Group drafts/previews stay off. This code and its local fake-platform tests
do not authorize a real group deployment.

## Telegram

[`telegram.Bot`](../../internal/channel/telegram/telegram.go) is on when the settings
have a bot token. By default, it reads messages with `getUpdates` long polling,
so dex needs no public URL. To use webhooks, see [Inbound webhooks](#inbound-webhooks).

- Private input still enters Main. Group and supergroup input uses an isolated
  audience for a current paired sender. `requireMention` is off by default; when on,
  group input must mention this bot or reply to it.
  The transport ignores other users, bot/anonymous senders and untriggered group text.
  It checks the current control allowlist before any file download. Group senders
  receive no private owner tools. A saved token without allowed users waits for pairing.
- The Telegram channel page creates a link with the bot username from `getMe` and a
  random code. The code expires after 10 minutes. `telegram_settings` stores
  the code and expiry. Pairing is available only while the allowlist is empty. One atomic SQLite
  update sets the owner as the only allowed user and clears the code.
  Invalid or expired codes create no user or chat. A new link or settings
  change cancels the previous code. Manual user-ID entry remains available.
  An allowed user who repeats a pairing command receives an already-paired
  reply, including after a lost acknowledgement or restart.
- The bot registers `/menu`, `/status`, `/settings`, `/sessions`, `/workflows`,
  `/question`, `/clear`, `/stop`, `/feedback`, and `/help` with `setMyCommands`.
  The channel manager owns command execution and deduplication.
- Each update has a stable source key: `telegram:<bot id>:<update id>`.
  A repeated update does not create another submission. An edited message creates
  a new correction input. It does not replace the earlier input or undo its actions.
  Reply, quote, and forward context cannot change sender authorization. Attachments
  and forwarded or edited command text do not execute chat controls. Feedback
  discards captions and forwarded text from its draft.
- Goldmark parses all text into versioned Rich Message HTML parts, including
  plain replies, lists, code, tables, menus and text notifications. `sendRichMessage`
  sends each frozen part. Lists keep their ordered or bullet markers. Raw HTML
  stays escaped text. Markdown images keep linked alt text; they do not request
  remote media. Link URLs have a 1024-byte budget after HTML escaping.
  The renderer limits each part to 32768 decoded Unicode characters and 500
  blocks. Formatting stays within 16 nesting levels. Native tables have at most
  20 columns; row splits repeat the header. A table with more columns or a row
  that cannot fit with its header becomes labeled rich paragraphs before freezing.
  Long text and code cells split without truncation. No API error selects a
  different text format. Unknown responses and 5xx retry the same frozen method;
  429 retains the existing account and outbox wait.
  Confirmed message IDs retain their actual kind, including copy and forward
  results. Album IDs do not use part indexes.
- Each pending submission has a native watch through `live.Hub`. The bot
  updates a private rich draft at most once per second with `sendRichMessageDraft`.
  If drafts fail, the bot sends a rich preview and edits it. Final replacement
  and tool edits use `editMessageText.rich_message`. A definite missing preview
  can create a new rich final message; an unknown edit cannot change methods. Typing refreshes every 4 seconds. Previews
  are best effort. Queued user items do not start answer segments. The final
  reply stops and joins its preview before delivery. Runtime shutdown removes
  preview messages that have no final owner. Preview registration and final
  lookup use one lock; attachment downloads do not hold this lock.
  Saved `options.preview` can select drafts, message edits, or no previews and
  no typing. `options.silent` applies to new text, media, and copied or forwarded
  messages. `options.reactions` is on by default for private chats. An explicit
  `false` stays off. `options.groupReactions` is a separate opt-in and does not
  change sender or topic authorization.
- Incoming rich messages keep bounded text from paragraphs, inline formatting,
  tables and nested blocks. The iterative reader caps text at 65536 characters,
  work at 20000 steps and JSON nesting at 64; a limit adds an explicit note.
  Rich input has a content label so embedded slash commands stay text. It does
  not fetch embedded media. Full rich quote fidelity is not implemented.
- Photos use the largest size. Documents, photos, voice, audio, video, video notes,
  animations, and stickers pass through `upload.Store`, with a 20 MiB limit.
  Captions become message text. The agent and web transcript use the same upload
  record. Location, contact, and poll inputs keep their available fields as text.
  Quoted files are not downloaded. Media bytes alone do not prove that a model
  can understand their content.
- Incoming albums persist each member before polling can acknowledge it. A batch
  closes after 800 ms without a new member, 3 seconds from its first member, or
  ten files. Admission uses the existing native input path. Source aliases stay
  available after temporary input data is cleared. Late members form an explicit
  continuation. Compatible outgoing files use `sendMediaGroup` in batches of 2–10.
  An explicit API 400 saves fallback mode before single sends. Each single send
  then has its own durable cursor. Lost or incomplete responses and HTTP 5xx do
  not permit a method change. Unconfirmed delivery is at least once.
- Transcription is off by default. `options.stt` requires a saved custom
  provider, an audio model, and consent to send audio. The provider must have
  an explicit base URL and support OpenAI-compatible `/audio/transcriptions`.
  Its chat model does not prove audio support. Agent speech output is not
  available; dex keeps the old `options.tts` field for API compatibility. Provider credentials use the existing catalog
  resolver; explicit `op://` provider keys use the configured 1Password CLI and
  stay references in storage. Channel settings contain no second API key. HTTPS is required except
  for loopback HTTP. Redirects, provider fallback, and automatic provider retries
  are disabled. Audio can leave the host and can incur provider charges.
- Transcription defaults are 30 seconds per request, 20 MiB per input or response,
  and 300 seconds of Telegram-reported input duration.
  STT does not decode audio to verify duration. `telegram_effects` retains staged
  upload ownership and provider results before native admission. It clears staged
  references after a business or album receipt owns the upload. Empty, failed, or
  unknown transcription keeps the original audio and an explicit note. Unknown
  requests are not sent again on recovery. These rows record side effects;
  they do not identify artifacts or queue work.
- Polling checks the token through `getUpdates`. A transient startup failure
  does not stop the bot. Poll errors and recovery update runtime status in
  bot settings. Tokens do not appear in status reasons.
- Pairing and attachment-rejection replies skip permanent 400/403 failures
  so later updates can proceed. Transient failures leave the update for retry.
  A failed input attempt removes its new uploads when no stored receipt owns
  them. An uncertain receipt lookup keeps the files.

## Inbound webhooks

Webhooks are optional for Telegram and Feishu/Lark. Existing installations do
not need a public listener. Set the transport separately for each bot's channel
through its authenticated settings API. If a request omits `transport`, dex
keeps the saved transport for the same platform account. The Channels page can
still update the other settings without changing the transport.

### HTTPS and reverse proxy

Keep dex on its private address (default `127.0.0.1:8787`). Use a public DNS
name and a TLS reverse proxy with a trusted certificate. Expose only the
needed callback paths. For example, for the `default` agent, a Caddy site can
use these rules:

```caddyfile
hooks.example.net {
    @channelHooks path /hooks/channels/telegram/default /hooks/channels/feishu/default
    handle @channelHooks {
        reverse_proxy 127.0.0.1:8787
    }
    handle {
        respond 404
    }
}
```

Replace `default` with the target agent ID. Remove paths for channels you do
not use. Forward the POST body and provider headers without changes. Do not
require the dex access token on callback paths. Dex authenticates those requests
with the platform credentials. Do not log callback bodies, security headers,
or settings requests. Allow callback bodies up to 4 MiB, the dex limit. Feishu
allows 3 seconds for the response. Do not add a proxy authentication redirect
or wait for an agent result.

### Telegram setup and rollback

1. Configure the public HTTPS route. Use port 443 with a trusted certificate.
   Telegram also lists ports 80, 88, and 8443 for webhooks. Dex does not upload
   a self-signed certificate.
2. Send `PUT /api/agents/{agentId}/channels/telegram` with the following fields.
   Keep the current `allowedUsers` list. An empty `token` keeps the saved bot
   token. Generate a random `secretToken` that meets the
   [Bot API requirements](https://core.telegram.org/bots/api#setwebhook).

   ```json
   {
     "token": "",
     "allowedUsers": [123456789],
     "transport": "webhook",
     "webhook": {
       "url": "https://hooks.example.net/hooks/channels/telegram/default",
       "secretToken": "REPLACE_WITH_A_RANDOM_SECRET"
     }
   }
   ```

3. Check the Telegram runtime status and send a message from an allowed user.
   Dex stops polling before it registers the callback with `setWebhook`. The
   settings response confirms the saved configuration. The runtime retries
   registration on failure and registers again on restart. It checks
   `X-Telegram-Bot-Api-Secret-Token` on each POST.

To roll back, send the same settings request with `"transport": "polling"`
and omit `webhook`. Dex stops the HTTP receiver and calls `deleteWebhook`
before it saves the change and starts polling. If Telegram does not confirm
teardown, the request fails and dex restores the webhook receiver. If the
outcome is uncertain, check `getWebhookInfo` and retry the settings change.
When you remove a webhook-enabled Telegram integration, dex also deregisters
it. A normal process stop leaves the registration in place for restart.

Both registration and teardown use `drop_pending_updates: false`. Dex requests
one callback connection to limit concurrent delivery. Retries can still arrive
out of order. Telegram
keeps undelivered updates for at most 24 hours and limits webhook delivery
attempts. A switch does not replay updates that Telegram already confirmed or
expired. If another client registered the bot before dex owned it, remove that
registration without dropping updates before you start dex in polling mode.

### Feishu and Lark setup and rollback

1. In the platform's developer console, open **Events & Callbacks** for the
   app. Copy its Verification Token. If you set an Encrypt Key, copy that key
   as well. Use the same security settings on both sides. Prefer encryption
   because the token is in the body of unencrypted events.
2. Send `PUT /api/agents/{agentId}/channels/feishu`. Keep the app ID and
   brand (`feishu` or `lark`). An empty `appSecret` keeps the secret for the
   same app. Use `"transport": "webhook"` and
   `"webhook": {"verificationToken": "REPLACE_WITH_PLATFORM_TOKEN"}`.
   Include `encryptKey` in `webhook` only if the platform uses one. Dex stops
   the WebSocket when it applies this configuration.
3. In **Event Subscriptions**, select the option that sends events to your
   server. Use `https://hooks.example.net/hooks/channels/feishu/default` as
   the Request URL. Keep the `im.message.receive_v1` subscription. Save the
   settings and release the app version if the console requires it. Dex
   answers the platform's `challenge` only after it verifies the token. With
   an Encrypt Key, the challenge must also be encrypted. Event requests in
   this mode must have a valid `X-Lark-Signature`. Dex uses the Lark SDK for
   signature calculation and decryption.

To roll back, send the settings request with `"transport": "websocket"`
and omit `webhook`. Dex closes the HTTP receiver and starts the WebSocket
receiver after it drains accepted events. In the developer console, switch
**Event Subscriptions** to persistent connection and remove the developer
server URL. Save and release the change as required. Dex does not change the
Feishu or Lark console on your behalf. If you remove the integration, also
remove its webhook in the console.

Schedule the switch during low traffic. Feishu retries a failed event up to
four times, at intervals of 15 seconds, 5 minutes, 1 hour, and 6 hours. The
platform does not promise to replay those events across subscription mode
changes. Check the platform's event logs after a switch. See the
[Feishu event overview](https://open.feishu.cn/document/server-docs/event-subscription-guide/overview).

### Intake and recovery

Dex commits each authenticated event to the channel webhook inbox in
`control.sqlite` before it returns HTTP 200. A failed commit gets HTTP 503.
The ACK does not wait for message processing, downloads, or agent execution.
Telegram uses the bot ID and update ID. Feishu uses the app ID and
`header.event_id`. The unique key also includes the platform. Valid retries
within the deduplication window get HTTP 200 without adding another item, even
after a restart. A URL verification challenge does not enter the inbox.

One worker per integration processes committed events in intake order. The
worker calls the same receive function as polling or WebSocket. The current
sender grants, pairing, and routing rules still apply. A transient failure can
delay later events. On success, dex clears the payload. A background job runs at
startup and every hour. It prunes handled IDs more than 7 days and 5 minutes
after intake. This covers both platforms' retry windows and Feishu's clock skew.
The job never prunes pending events.

Feishu webhook events can be at most 7 days old or 5 minutes ahead of the
host clock. Dex checks the signed request timestamp in encrypted mode and also
checks `header.create_time` when present. Token-only mode requires
`header.create_time`. For Telegram, the secret header is the security boundary. Dex cannot use the update timestamp to reject replays
once the deduplication record expires. Keep the secret private and rotate it
if exposed.

The inbox, not platform replay, recovers accepted events
after restart. The default transport drains an existing inbox before it starts
receiving from the platform. It skips events already processed from the inbox
when the platform sends the same event ID again. Pending payloads stay bound to the agent that
accepted them and are not transferred when credentials move to another agent.
Back up and restrict access to `control.sqlite`: it can contain pending
message bodies and channel credentials.

## Shared channels

The app owns a registry with one Manager per agent. Startup restores all agents' bots.
Creating an agent adds its Manager. Shutdown stops all Managers before the database closes.
Uploads and live previews use the bot's agent directory and stream. Chat notifications
wake all Managers; each Manager reads only its agent's replies and commands. This fan-out
is a limit: route notifications by agent if wakeups become costly.

Channel settings use `agent_id` as their primary key. Telegram bot IDs, WeChat bot IDs,
and Feishu app IDs cannot be assigned to multiple agents. A duplicate returns 409 and names the
agent that owns the credential. QR login and registration sessions are also per agent.
Migration maps old settings, peers, and commands to `default`. Rollback keeps only
that agent's channel data. WeChat runtime remains keyed by bot ID and owner.

[`delivery.Manager`](../../internal/channel/delivery/manager.go) owns bot lifetime,
commands, and reply delivery. Settings changes stop the old bot
before they start its replacement. Settings expose runtime status: `off`,
`running`, `error` with a reason, or `needs_login`.

The root `channel` package defines the transport contract and frozen delivery
values without storage or runtime dependencies. The app passes the manager to
each adapter through the inbound interface that adapter defines. The delivery
package does not import concrete adapters. Telegram rich text, buttons, and
media groups keep their shared frozen encoding; the adapter owns the wire format.

`chat.Service.ReceivedSource` reports the global stored input fact. Channel
and platform adapters use it for duplicate input and attachment replay, including
when a bot credential moves between agents. `LookupSource` returns the
business-owned `SourceReceipt` for reply previews and upload cleanup. Its
references belong to the requested agent. A stored receipt means control
received the input, even when native admission is pending. Adapters do not infer
this state from `admitted_at` or terminal status. Native admission remains the
execution authority; reconciliation still retries pending input with its
stable request ID. Channel intake advances after an ordinary input commits its
source receipt. It does not wait for a native admission attempt. Deferred input
uses the same authorization and Session queue. Commands still use their own
receipts and acknowledgements. Polling and the outbox serialize replays of the
same command by source key. They do not hold a Bot-wide command lock. A native rejection settles the input receipt
without setting `admitted_at`.

A peer names a remote destination, such as `telegram:7`. Owner input from all
channels enters the bot's agent `main` conversation with that peer as origin.
Channel input cannot select a Chat session. Old `channel_peers` records and
per-peer conversations remain stored but receive no new channel input. The
Sessions list excludes those conversations. Main and Chat are writable on
the web. Automation transcripts have no web composer.

`/clear`, `/new`, `/stop`, and `/help` use durable command
receipts. `/clear` and its alias `/new` use `channel:<source key>` as the stable
reset request ID.
A crash after reset admission but before command completion reuses that reset.
A busy refusal is saved in the command receipt; replay cannot clear it later.
The command sends one short notice. Reset submissions have no model reply.
An incomplete command with an old peer channel key gets a notice with no action.
`/stop` records stop intent for main and
withdraws queued input and pending results. A channel Stop saves its answer
in the same transaction as the withdrawal. If command completion fails,
replay uses that answer without another withdrawal or native abort. The reply
confirms the cancellation request and states that processes and remote
work can continue. A stop error does not block later messages or other replies.

`/feedback` is independent of the model and conversation. In a paired private
chat, send `/feedback Title` on the first line and the description on the lines
below. The reply shows only those fields and gives a clean link to the public
`recally-io/dex` new-issue page. The user copies the draft and submits it in
GitHub. Telegram groups and topics apply their usual sender and mention gates
and receive only a private-chat prompt. Dex does not save their command text
or download any media attached to that command. Drafts with Markdown code fences
or drafts too large for one reply can use the Web form instead.

Feedback uses the existing command receipt to deduplicate incoming updates
and retry delivery. Until delivery ends, the pending receipt holds the private
reply in `control.sqlite`. Dex clears the reply from the receipt when it is
sent or permanently skipped; the source key and routing facts remain. There is
no feedback store, model input, or GitHub write. The reply remains on the
messaging platform like any other message.

Each platform has one durable sender. Settlement wakes the origin's sender.
Legacy Stop wakes senders for withdrawn input. At start
and after a failed send, it reads settled submissions and command replies
that remain undelivered, in submission admission order. It records each confirmed part and marks the reply
delivered after its last part. A permanent rejection skips that part and does
not block later replies. A transient error retries with a delay from 5 s to
60 s. Each bot receives a stable delivery ID and part index through `Bot.Send`.
Receipt reconciliation reads pending input and incomplete native abort intent.
Delivery is at least once: a crash between platform success and the local
commit can duplicate one part. Migration marks old settled replies as delivered
because earlier versions did not record delivery success.

Telegram and Feishu/Lark can show status on an authorized incoming message.
`delivery` starts observing only after the input intent commits. The native
submission must enter a run before processing appears. Success needs a settled
input and confirmation of every part of its required reply, if any. A skipped
part is not confirmation. Failure and cancellation use distinct reactions.
Access is checked again before any reaction. A short-lived observer serializes
changes for its message; slow, rejected, and unknown reaction requests never
control intake or the reply sender's retries. Commands and Telegram edits do not reset a message's status. Dex can drop reactions under
load, after a timeout, or when the bot stops. It does not retry or replay them.

### Promised follow-ups

On a direct channel request, the parent can set `follow_up: true` on
`session_new` or `session_send`. The commitment belongs to that delegation,
request, session, and channel authorization. It does not change group tool
permissions or turn an acknowledgment or progress update into a conclusion.

The original child-result turn tells the parent to use `notify_user`
or `ask_user`. Only explicit user-facing actions can enter the
channel Outbox. A question does not complete the commitment. Plain final text
stays internal. Dex records `missed_follow_up` without creating another turn,
retrying the omission, or forwarding any draft or child output.

The control store records the commitment separately from delivery. A `notified`
commitment means an explicit conclusion entered the Outbox, not that the channel
accepted it. Use the linked Outbox receipt, including its skipped-part and error
information, for delivery confirmation. Stop and Clear cancel outstanding
commitments and revoke their pending Outbox entries in the same transaction.
If an already-started send succeeds, Dex still records its platform receipt.

Channel revocation stays on the original input even if its reply was already
delivered. It also applies to existing and future child results, with or without
a follow-up commitment. Restoring the channel grant allows new inputs but does
not authorize delivery from the old result.

## Feishu and Lark

[`feishu.Bot`](../../internal/channel/feishu/bot.go) receives private messages through
one WebSocket long connection by default. It needs no public callback URL unless
you select [webhooks](#inbound-webhooks). The REST
client uses the official Lark Go SDK for token requests, message creation,
card edits, and reactions. Dex owns the token cache for each client, response
limits, and retries. It passes an explicit token to the SDK so that the SDK
does not refresh tokens and replay mutations. Dex streams message resources
without buffering them in the SDK.

The transport uses `coder/websocket` and the SDK's protobuf frame codec.
Dex reads one event at a time and acknowledges it on the same socket after
processing. Dex also handles ping, pong, fragmented events, and reconnect hints.
Startup failures report an error and retry with the REST backoff, up to
2 seconds between attempts. A configured bot retries until cancellation,
even if the server sends a finite `ReconnectCount`. After a disconnect, the
first reconnect waits only for the server's jitter. Later failures use the
server's reconnect interval. A socket with no data for two ping intervals
reconnects. A pong that changes the ping interval also resets the next ping.
The ping timer and read deadline use the same interval.
Fragment buffers expire after 5 seconds. One connection allows at most 64
fragmented events, 256 fragments per event, and 4 MB per event.

The Feishu channel page shows runtime status and supports QR app creation and
manual credentials. The form keeps the saved Feishu or Lark brand unless the
user changes it. QR setup requests `im:message`, `im:message.p2p_msg:readonly`, `im:resource`, and the
`im.message.receive_v1` event. The server keeps the device code in a session
until approval or expiry. Registration requests release the settings lock
while they wait for the accounts service. Cancellation or replacement rejects
late responses. Lark tenants use the Lark accounts service. Setup
saves the creator's `open_id` with the app credentials through
`settings.Service`. Manual setup requires a one-time code sent in a private
message. The code expires after 10 minutes. A credential change clears owner
authorization. The settings API never returns the saved app secret.

Private status reactions are on unless `reactions` is explicitly false. The
existing `im:message` permission covers adding and deleting reactions. Dex does
not request new scopes. When a permission or a message does not allow a
reaction, replies still work. Feishu and Lark need a delete followed by an add
to replace a reaction. Dex uses the ID returned by the add; it does not list
reactions or remove a user's reaction. Restart can leave the old reaction in
place.

The bot ignores groups and other users. Text mentions are removed. Rich posts
become text and image attachments. Code blocks keep their text and language
in Markdown fences. Repeated `/pair` messages never reach the agent.
Message resources pass through `upload.Store`, with its 20 MB file limit and 10 attachments per message.
The shared channel manager handles `/clear`, `/new`, `/stop`, `/help`, and `/feedback`. A source key contains `feishu:<app ID>:<message ID>`. The WebSocket receiver acknowledges valid agent
input after durable admission succeeds. Invalid content, rejected resource access,
and files with invalid names or excessive size are acknowledged and ignored.
Storage and temporary platform failures get a 500 acknowledgement for retry.
A rejected resource removes uploads already saved by that attempt. After input
admission, cleanup removes files only when no stored receipt owns them. An
uncertain receipt lookup keeps the files.

A preview card reads the chat's native watch events. It merges text updates
at 250 ms intervals and omits thinking and tool data. The shared durable
sender patches that card with the final answer, or creates a final card when
no preview exists. It uses the submission ID and part index for stable send
UUIDs. Each text part is at most 4000 UTF-8 bytes. A card rejected with HTTP
400 or an HTTP success status and code `230001` falls back to plain text.
Temporary or uncertain send
failures keep the card format and stable identity for retry. An uncertain final
patch keeps the preview ID, so durable delivery retries the same card. A successful
final patch restores `running` status.
Network failures, server errors and rate limits get at most
3 attempts per token. JSON calls and downloads share authorization and one
token refresh. A token rejection can therefore cause up to 6 API attempts,
plus token requests. Downloads wait at most 30 seconds for response headers;
each complete download has a 5-minute deadline, including error body reads.
WebSocket downloads use the socket session context. Webhook downloads use the
bot context.
Durable admission uses the bot lifetime, so a disconnect cannot cancel a stored
message while it waits for queue space. JSON responses remain
bounded to 4 MB and download error responses to 1 MB. Retry delays are
200 ms and 800 ms; `Retry-After`
can extend a delay to at most 2 seconds. Reply delivery remains at least once.
Previews use the bot lifetime and continue across WebSocket reconnects.
Previews are best effort and do not mark a reply as delivered. A restart can
leave a preview card in the platform; the durable sender sends a final card.

The standard `mise run check` task includes local platform tests and native HTTP
E2E tests for Feishu. These tests make no real platform requests.

## WeChat

[`wechat.Bot`](../../internal/channel/wechat/bot.go) connects one iLink bot to its
owner's private messages. The WeChat channel page starts QR login and polls each
state. It asks for a phone verification code when required. A region redirect
changes the poll host. An expired or blocked QR code gets a replacement, with
at most three replacements per login attempt. The backend queries QR status only
when the page requests a poll. Closing the page stops new polls; an in-flight
request ends within 35 s. There is no fixed deadline for the whole login attempt.

Only a confirmed result with a bot token, bot ID, base URL, and owner ID can
activate the bot. Invalid confirmation stays failed on later polls. Valid
candidate credentials remain available if local activation fails. Activation
stops and joins the old bot before it changes shared runtime state. It saves
credentials and runtime initialization in one transaction, starts the replacement,
and then reports `confirmed`. If the transaction fails, it restores the previous
bot. A new login ID or delete invalidates an older login request.

The bot ignores other senders, group messages, bot echoes, and unfinished
messages. It stores the owner's latest context token before it admits a
submission. Each source key uses the bot ID and message ID. The bot advances
the durable update cursor only after every message in the batch is stored or
explicitly rejected. A repeated batch therefore cannot run a submission twice.
Cursor, context token, and pause time are runtime state in `control.sqlite`.
The settings API does not return them. Login initializes the runtime row; bot
reads use SELECT. A missing row is a storage invariant error and retries with
an error status.

Message IDs retain the full protocol `uint64` range. Context watermarks use
20-digit decimal TEXT so SQLite can compare them without signed truncation.
The context guard retains the existing assumption that larger message IDs are
newer. The [official Tencent client 2.4.10](https://registry.npmjs.org/@tencent-weixin/openclaw-weixin/-/openclaw-weixin-2.4.10.tgz)
declares `uint64` IDs but provides no
numeric chronology guarantee in its protocol types or receive loop. This
assumption remains unverified; no new ordering mechanism is introduced.

Text and voice transcription become message text. Quoted text gets a
`[引用: ...]` prefix. A video becomes a placeholder. Images and files pass
through CDN download and AES-128-ECB decryption into `upload.Store`, with the
same limits as web uploads: 10 files per message and 20 MB per file. Invalid
media is rejected. CDN HTTP 408 and 429 are temporary failures. A temporary
download or storage error keeps the batch cursor unchanged. Existing submission
and command receipts are checked before attachment downloads. An interrupted
command still completes through the shared manager. A failed or rejected attempt
removes its new uploads before admission. Admitted submissions keep their files.

While a submission is pending, the bot sends typing status every 5 s. Final
answers keep supported Markdown and use parts of at most 2000 characters.
Unanswered submissions send a short error message. The durable sender reads
the saved context token before each send, including after a restart.
