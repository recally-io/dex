# Channels

This document describes the channel adapters and how they share delivery.

## Capabilities

The [root `channel` package](../../internal/channel/contract.go) defines the transport
contract and frozen delivery values. It depends only on the Go standard library.
Each adapter implements `Bot` and defines its own inbound interface. The manager
uses these optional capabilities:

- `Reactor`: Telegram and Feishu can show status on incoming messages. The app
  binds their reaction authorizers. Without an authorizer, the manager sends no
  reactions for that platform.
- `WebhookReceiver`: Telegram and Feishu can receive HTTP callbacks. A nil
  `InboundWebhook` result means webhooks are off for the current configuration.
- `InboundWaker`: Telegram can wake its album collector. The manager calls
  `WakeInbound` under a lock, so it must not block.

## Telegram

[`telegram.Bot`](../../internal/channel/telegram/telegram.go) is on when the settings
have a bot token. By default, it reads messages with `getUpdates` long polling,
so dex needs no public URL. To use webhooks, see [Inbound webhooks](#inbound-webhooks).

- Paired private chats share Main. The adapter accepts messages, edits, and
  callbacks only from private chats. A token with no allowed user waits for pairing.
- The Telegram channel page creates a link with the bot username from `getMe` and a
  random code. The code expires after 10 minutes. `telegram_settings` stores
  the code and expiry. Pairing is available only while the allowlist is empty. One atomic SQLite
  update sets the owner as the only allowed user and clears the code.
  Invalid or expired codes create no user or session. A new link or settings
  change cancels the previous code. Manual user-ID entry remains available.
  An allowed user who repeats a pairing command receives an already-paired
  reply, including after a lost acknowledgement or restart.
- Managed setup uses one dedicated manager bot per Dex server. The owner sets
  its token on the Telegram channel page. The manager cannot also be a channel
  bot and must have no other poller or webhook. SQLite stores its credential
  reference. Dex checks `getMe.can_manage_bots` before it offers setup and again
  when setup starts. It polls for `message` and `managed_bot` updates. A change
  to the manager cancels pending setups without changing saved channels.
- A managed setup lasts 10 minutes. The owner taps Start in the manager's
  private chat through a one-time link. Dex records that sender as the owner
  and then shows the official `t.me/newbot` link. Telegram sends no setup ID
  in `managed_bot`, so Dex matches `user.id` and the exact random bot username.
  The owner can change the display name. A different username does not connect.
- SQLite binds the new `bot.id` to one setup. Dex calls `getManagedBotToken` and
  checks the returned token with `getMe`. It writes the token to the private
  vault before it commits the channel and setup state together. Duplicate
  updates for a finished setup do not fetch the token again. The replacement
  keeps the saved transport and webhook settings. Before reusing a webhook URL
  and secret, Dex stops the old receiver and removes its registration. If removal
  or the local commit fails, Dex restarts the saved receiver. Cancellation,
  expiry, failure, or a manual channel save leaves the saved settings unchanged
  by that setup. Only a confirmed setup starts the channel with the new bot and
  its verified owner as sole sender.
  A crash before the SQLite commit can leave an unused encrypted credential.
- The bot registers `/menu`, `/status`, `/settings`, `/sessions`, `/workflows`,
  `/question`, `/clear`, `/stop`, `/feedback`, and `/help` with `setMyCommands`.
  The channel manager owns command execution and deduplication.
- Each update has a stable source key: `telegram:<bot id>:<update id>`.
  A repeated update does not create another submission. An edited message creates
  a new correction input. It does not replace the earlier input or undo its actions.
  Reply, quote, and forward context cannot change sender authorization. Attachments
  and forwarded or edited command text do not execute session controls. Feedback
  discards captions and forwarded text from its draft.
- Goldmark renders one Rich Message for each text delivery. Lists preserve their markers. Raw HTML stays escaped. Markdown images keep linked alt text and do not fetch remote media. Long answers send an extract with a reference to Web, which keeps the full answer. Unknown responses retry the same message format.
- Telegram refreshes native typing status every 4 seconds while an input is pending. `options.typing` can turn it off. Telegram does not send drafts or edit temporary answer messages. `options.silent` applies to new text and media. `options.reactions` is on by default for private chats.
- Incoming rich messages keep bounded text from paragraphs, inline formatting,
  tables and nested blocks. The iterative reader caps text at 65536 characters,
  work at 20000 steps and JSON nesting at 64; a limit adds an explicit note.
  Rich input has a content label so embedded slash commands stay text. It does
  not fetch embedded media. Full rich quote fidelity is not implemented.
- Photos use the largest size. Documents, photos, voice, audio, video, video notes,
  animations, and stickers pass through `upload.Store`, with a 20 MiB limit.
  Captions become message text. The bot and web transcript use the same upload
  record. Location, contact, and poll inputs keep their available fields as text.
  Quoted files are not downloaded. Media bytes alone do not prove that a model
  can understand their content.
- See [file delivery and albums](files.md#telegram) for batch limits, replay,
  and outgoing file snapshots.
- Transcription is off by default. `options.stt` requires a saved custom
  provider, an audio model, and consent to send audio. The provider must have
  an explicit base URL and support OpenAI-compatible `/audio/transcriptions`.
  Its chat model does not prove audio support. Provider credentials use the catalog
  resolver; explicit `op://` provider keys use the configured 1Password CLI and
  stay references in storage. Channel settings contain no second API key. HTTPS is required except
  for loopback HTTP. Redirects, provider fallback, and automatic provider retries
  are disabled. Audio can leave the host and can incur provider charges.
- Transcription defaults to 30 seconds per request, 20 MiB per input, and
  300 seconds of Telegram-reported duration. The response limit is the smaller
  of 64 KiB and the configured byte limit. Dex does not decode the audio to
  verify duration. `telegram_effects` retains staged
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

### Private controls

Telegram `/menu`, `/status`, `/settings`, `/sessions`, `/question` and
`/workflows` use Main. The session list shows the bot's direct child sessions.
`/run <child ID> [message]` queues another parent-owned turn in a saved child.
Only the Telegram sender who started the child can resume it.

Inline buttons carry a 96-bit HMAC reference signed with the current bot token. An immutable
server row binds the actor, account, private chat, confirmed message, conversation,
native ID, context-reset receipt, object, action and a 15-minute expiry. The claim
transaction checks current authority and consumes the nonce with its durable command intent.
Command processing and effect transactions use one read-only validity check
(`session.ValidateTelegramControl`); effects repeat it under the SQLite writer. It covers the
current grant, expiry, actor, message, object and context revision. A definite 400 or 403
rejection of the final `answerCallbackQuery` does not hold the polling offset; network, 5xx
and 429 errors retry the same update, and the consumed nonce cannot queue a second intent.
Clear does not change the native conversation ID, so callbacks also bind the latest
`context_reset` receipt. Replays cannot queue another action. A matching actor's rejected grant cancels its
button permanently. Command parts and button labels stay frozen across delivery retries.
Message delivery stays at least once; only the bound confirmed remote message accepts its
buttons. A stale Question button cannot become ordinary model input.

## Inbound webhooks

Webhooks are optional for Telegram and Feishu/Lark. Existing installations do
not need a public listener. Set the transport separately for each bot's channel
through its authenticated settings API. If a request omits `transport`, dex
keeps the saved transport for the same platform account. The Channels page can
still update the other settings without changing the transport.

### HTTPS and reverse proxy

Keep dex on its private address (default `127.0.0.1:8787`). Use a public DNS
name and a TLS reverse proxy with a trusted certificate. Expose only the
needed callback paths. This example does not expose the admin API. To serve the
admin API over HTTPS, follow the [authenticated HTTPS setup](../../README.md#deploy-an-https-entry-point). For example, for the `default` bot, a Caddy site can
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

Replace `default` with the target bot ID. Remove paths for channels you do
not use. Forward the POST body and provider headers without changes. Do not
require the dex access token on callback paths. Dex authenticates those requests
with the platform credentials. Do not log callback bodies, security headers,
or settings requests. Allow callback bodies up to 4 MiB, the dex limit. Feishu
allows 3 seconds for the response. Do not add a proxy authentication redirect
or wait for a bot result.

### Telegram setup and rollback

1. Configure the public HTTPS route. Use port 443 with a trusted certificate.
   Telegram also lists ports 80, 88, and 8443 for webhooks. Dex does not upload
   a self-signed certificate.
2. Send `PUT /api/bots/{botId}/channels/telegram` with the following fields.
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
2. Send `PUT /api/bots/{botId}/channels/feishu`. Keep the app ID and
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

Dex commits each authenticated event to `channel_webhook_queue` in
`control.sqlite` before it returns HTTP 200. A failed commit gets HTTP 503.
The ACK does not wait for message processing, downloads, or bot execution.
Telegram uses the bot ID and update ID. Feishu uses the app ID and
`header.event_id`. The unique key also includes the platform. Valid retries
within the deduplication window get HTTP 200 without adding another item, even
after a restart. A URL verification challenge does not enter the queue.

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

The Webhook Queue recovers accepted events without platform replay
after restart. The default transport drains the existing queue before it starts
receiving from the platform. It skips events already processed from the queue
when the platform sends the same event ID again. Pending payloads stay bound to the bot that
accepted them and are not transferred when credentials move to another bot.
Back up and restrict access to `control.sqlite`: it can contain pending
message bodies and channel credentials.

## Shared channels

The app creates one Manager for each active Bot. The Manager runs that Bot's
configured channels. Shutdown stops all Managers before the database closes.
Uploads use the Bot's workspace. Session notifications
wake all Managers; each Manager reads only its bot's replies and commands. This fan-out
is a limit: route notifications by bot if wakeups become costly.

Channel settings use `bot_id` as their primary key. Telegram bot IDs, WeChat bot IDs,
and Feishu app IDs cannot be assigned to multiple bots. A duplicate returns 409 and names the
bot that owns the credential. QR login and registration sessions are also per bot.
WeChat runtime state is keyed by bot ID and owner.

[`delivery.Manager`](../../internal/channel/delivery/manager.go) owns bot lifetime,
commands, and reply delivery. Settings changes stop the old bot
before they start its replacement. Settings expose runtime status: `off`,
`running`, `error` with a reason, or `needs_login`.

The app passes the manager to Feishu and WeChat. For Telegram, the app
combines the manager with the [Telegram policy](../../internal/channel/telegram/policy/)
and registers that policy with the manager. Feishu and WeChat do not register
a policy. The delivery package and the Telegram policy do not import each other.
The policy owns Telegram command rules, buttons, callback claims, speech, album
cleanup, file queueing, and queued reaction acknowledgements. Telegram rich text,
buttons, and files keep their shared frozen encoding. The adapter owns the wire
format.

`session.Service.ReceivedSource` reports the global stored input fact. Channel
and platform adapters use it for duplicate input and attachment replay, including
when a bot credential moves between bots. `LookupSource` returns the
business-owned `SourceReceipt` for typing and upload cleanup. Its
references belong to the requested bot. A stored receipt means control
received the input, even when native admission is pending. Adapters do not infer
this state from `admitted_at` or terminal status. Native admission remains the
execution authority; reconciliation still retries pending input with its
stable request ID. Channel intake advances after an ordinary input commits its
source receipt. It does not wait for a native admission attempt. Deferred input
uses the same authorization and Session queue. Commands still use their own
receipts and acknowledgements. Polling and the outbox serialize replays of the
same command by source key. They do not hold a Bot-wide command lock. The
manager checks the policy's peer constraint before source deduplication. It
looks up a command receipt after source deduplication. The Telegram policy
checks the actor, target, and context revision in the callback claim
transaction. The Session service checks them again in the effect transaction.
A native rejection settles the input receipt without setting `admitted_at`.

A peer names a remote destination, such as `telegram:7`. Owner input from all channels enters Main with that peer as origin. Dex checks
pairing at intake. The receipt saves the account and peer. Ordinary channel
input cannot select a Session. See [reply routing](sessions.md#input-and-replies).

`/clear`, `/new`, `/stop`, and `/help` use durable command
receipts. `/clear` and its alias `/new` use `channel:<source key>` as the stable
reset request ID.
A crash after reset admission but before command completion reuses that reset.
A busy refusal is saved in the command receipt; replay cannot clear it later.
The command sends one short notice. Reset submissions have no model reply.
`/stop` requests Session cancellation on Main. It withdraws queued input and
pending results. The command saves its answer in the same transaction as the
withdrawal. For the distinction from Main foreground Stop, see [Sessions](sessions.md). If command completion fails,
replay uses that answer without another withdrawal or native abort. The reply
confirms the cancellation request and states that processes and remote
work can continue. A stop error does not block later messages or other replies.

`/feedback` is independent of the model and conversation. In a paired private
chat, send `/feedback Title` on the first line and the description on the lines
below. The reply shows only those fields and gives a clean link to the public
`recally-io/dex` new-issue page. The user copies the draft and submits it in
GitHub. Drafts with Markdown code fences
or drafts too large for one reply can use the Web form instead.

Feedback uses the existing command receipt to deduplicate incoming updates
and retry delivery. Until delivery ends, the pending receipt holds the private
reply in `control.sqlite`. Dex clears the reply from the receipt when it is
sent or permanently skipped; the source key and routing facts remain. There is
no feedback store, model input, or GitHub write. The reply remains on the
messaging platform like any other message.

Each platform has one durable sender. All platform message bodies, including command acknowledgements and Automation alerts, go through the Outbox. Direct channel inputs can queue final replies or a short error. Ordinary Web and background output stay in the transcript. Background runs can call `notify_user` to queue a message before the run ends. The tool uses the configured default or an explicit paired channel. If a platform has multiple targets, specify `channel:target`.

The key `(event_id, channel, target)` identifies one message. An event can have independent deliveries to different destinations. The sender freezes the platform message before it sends. Confirmation marks it `sent`. Temporary errors keep it `pending`, with backoff from 5 to 320 seconds or the platform wait if longer. A permanent rejection marks it `failed`. A temporary failure also expires
when the row is at least 24 hours old and has at least 20 attempts.
No failure changes the destination. Feishu and WeChat use a stable key for retries. Telegram can duplicate a message if the platform accepts it before a crash and the receipt does not commit. The sender checks that the Outbox row is still pending immediately before each send. The Telegram policy binds callback buttons to the remote message in the same transaction that records confirmation.

Telegram and Feishu/Lark can show status on an authorized incoming message.
`delivery` starts observing only after the input intent commits. The native
submission must enter a run before processing appears. Success needs a settled
input and confirmation of its required reply, if any. Failure and cancellation use distinct reactions.
Access is checked again before any reaction. A short-lived observer serializes
changes for its message; slow, rejected, and unknown reaction requests never
control intake or the reply sender's retries. Commands and Telegram edits do not
reset a message's status. Dex can drop reactions under load or when the bot stops.
Only Telegram private-chat queued acknowledgements retry. Telegram must return a
429 that confirms rejection. Dex makes at most 3 attempts, with backoff of 1 s
and 2 s. Telegram's `retry_after` can extend either delay. No retry starts after
30 s or after the input leaves the native Inbox. Each retry checks current
sender authorization and the private reaction setting. If a request has an
unknown outcome, Dex stops sending reactions for that input. Dex does not replay
reactions after restart.

To inspect an acknowledgement, join `submissions.id` to
`telegram_queue_acknowledgements.submission_id` in `control.sqlite`. Find the
submission by its `source_key`. A missing acknowledgement row means no attempt
was recorded. Dex saves `unknown` before each request, including retries. A
crash at that boundary cannot leave the outcome of an earlier attempt in place.
`failed` records a definite rejection. `accepted` means Telegram confirmed the
request. It does not prove the user saw the reaction. `updated_at` is Unix time
in milliseconds.

### Background results and delivery diagnostics

Child results enter the parent Session as `session_result`. Monitors and
Processes send `wake` to their own Session. [Task wakes](tasks.md#coalesced-main-wakes)
enter Main as `task_wake`. These inputs have no automatic channel reply route.
Main can use `notify_user` to send an explicit notification.

For delivery diagnostics, read `control.sqlite`:

```sql
SELECT source_submission_id, disposition, peer, remote_ids
FROM outbox_delivery_diagnostics
WHERE id = :outbox_id;
```

- `platform_confirmed`: Dex saved at least one provider-returned remote ID
  for the recorded peer.
- `revoked_or_skipped_unconfirmed`: the send was revoked or skipped and
  there is no provider receipt.
- `unconfirmed`: there is no terminal evidence. Neither a pending send nor
  a `sent` status on a platform that does not return a remote ID proves confirmation. If a provider accepts a
  request and Dex stops before it saves the receipt, a retry can send again.

### Pairing and pending deliveries

Replies to one peer follow input order. Earlier pending inputs and replies
waiting for retry hold later replies. A terminal reply in `outbox_messages`, a
revoked input, or a settled input with no reply does not hold the queue.

The channel settings hold the current pairing. Unpairing marks pending deliveries to the removed destination `failed` in the same transaction. A new pairing for the same account has no generation ID. It does not revive cancelled deliveries. A request already in flight can still reach the platform.

## Owner channel tools

`channel_status`, `channel_pairing`, and `channel_notification_targets` read
one active bot at a time. The current bot is the default. The owner can use a
bot ID to select another active bot. The tools return runtime states, pairing
flags, and authorized notification destinations. They omit runtime error text,
credentials, pairing codes, and login details. The transcript omits raw tool
arguments. Argument errors do not echo rejected values.

A live owner turn in a top-level Main or session can use these tools.
Web and paired private input have the same owner access. A steer does not
change that access. A child session or background turn cannot use these tools.

`channel_request_change` can request a Telegram or Feishu pairing revocation,
a disconnect, or a change to the primary notification destination. WeChat
pairing ends when the owner disconnects the channel. Use Channels settings to
enter credentials, get a pairing link or code, or scan a login QR code.

The change tool has no apply operation. It returns a link to a server-rendered
review page. The page shows the stored operation, bot, and target. A same-origin
POST from the owner surface applies that request. When dex requires sign-in,
the review page requires the web session cookie. A session message or a tool call
cannot approve a request. The request belongs to its originating session and
expires after 10 minutes. Control consumes it once and checks that the session,
bots, and target are still valid. A notification destination must still map to
the peer shown on the page. Changed credentials or pairing make an earlier
request invalid. A restart clears requests that are still pending.

Control uses the same channel settings service as the web UI. It logs the
outcome of applied and denied mutations with the verified bot and operation
when available. It omits credentials, raw arguments, remote peer IDs, and
approval links. The approval link is a bearer capability given to the model.
The link does not isolate the owner on a no-token install or from code that
runs as the dex OS user.

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
fragmented events, 256 fragments per event, and 4 MiB per event.

The Feishu channel page shows runtime status and supports QR app creation and
manual credentials. The form keeps the saved Feishu or Lark brand unless the
user changes it. QR setup requests `im:message`, `im:message.p2p_msg:readonly`, `im:resource`, and the
`im.message.receive_v1` event. The server keeps the device code in a session
until approval or expiry. Registration requests release the settings lock
while they wait for the accounts service. Cancellation or replacement rejects
late responses. Lark tenants use the Lark accounts service. Setup
saves the creator's `open_id` with the app credentials through `bot.Service`. Manual setup requires a one-time code sent in a private
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
in Markdown fences. Repeated `/pair` messages never reach the bot.
Message resources pass through `upload.Store` with a 20 MiB limit per file
and 10 attachments per message. The shared manager handles ordinary commands.
The source key is `feishu:<app ID>:<message ID>`. The WebSocket receiver
acknowledges valid input after the business receipt commits. Invalid content, rejected resource access,
and files with invalid names or excessive size are acknowledged and ignored.
Storage and temporary platform failures get a 500 acknowledgement for retry.
A rejected resource removes uploads already saved by that attempt. After input
admission, cleanup removes files only when no stored receipt owns them. An
uncertain receipt lookup keeps the files.

Feishu sends one final card without a preview. Outbound media are not supported. A long answer sends an extract and keeps the full answer in Web. Feishu uses a stable UUID derived from the delivery ID. A definite card rejection with code `230001` allows a plain text fallback. Other errors do not change the format.
Network failures, server errors and rate limits get at most
3 attempts per token. JSON calls and downloads share authorization and one
token refresh. A token rejection can therefore cause up to 6 API attempts,
plus token requests. Downloads wait at most 30 seconds for response headers;
each complete download has a 5-minute deadline, including error body reads.
WebSocket downloads use the socket session context. Webhook downloads use the
bot context.
Durable admission uses the bot lifetime, so a disconnect cannot cancel a stored
message while it waits for queue space. JSON responses remain
bounded to 4 MiB and download error responses to 1 MiB. Retry delays are
200 ms and 800 ms; `Retry-After`
can extend a delay to at most 2 seconds. Reply delivery remains at least once.

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
credentials and runtime initialization in one transaction. A scan for the same bot and owner keeps the same destination. Dex starts the replacement and then reports
`confirmed`. If the transaction fails, it restores the previous
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

Message IDs use the full `uint64` range. SQLite compares zero-padded
20-digit text watermarks. Dex assumes that larger IDs are newer. Platform
ordering is unverified.

Text and voice transcription become message text. Quoted text gets a
`[引用: ...]` prefix. A video becomes a placeholder. Images and files pass
through CDN download and AES-128-ECB decryption into `upload.Store`, with the
same limits as Web uploads: 10 files per message and 20 MiB per file. Invalid
media is rejected. CDN HTTP 408 and 429 are temporary failures. A temporary
download or storage error keeps the batch cursor unchanged. Existing submission
and command receipts are checked before attachment downloads. An interrupted
command still completes through the shared manager. A failed or rejected attempt
removes its new uploads before admission. Admitted submissions keep their files.

While a submission is pending, the bot sends typing status every 5 s. Final
answers keep supported Markdown. Long answers send an extract and refer to Web.
Unanswered submissions send a short error message. The durable sender reads
the saved context token before each send, including after a restart. A temporary
receive or runtime read error retries after 5 seconds. Either `ret == -14` or
`errcode == -14` pauses calls for one hour, sets `needs_login`, and keeps pending
replies. The pause survives restart. A fresh QR login clears it and wakes the sender.
