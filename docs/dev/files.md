# Files and attachments

This document describes the bot artifacts directory, file results, channel
file delivery, and attachments.

## Artifacts and file delivery

Each bot has one artifacts directory at
`<data dir>/agents/<bot ID>/artifacts/`. Its immediate regular files are the
complete artifact list for every conversation in that bot. Names, sizes, and
modification times come from directory entries and stat. Artifact reads and
operations do not read or write the legacy artifacts table. No manifest,
conversation route, stable ID, operation receipt, deduplication, migration, or
deleted marker is used. Existing disk files stay untouched and use their current
physical names. Links and subdirectories are not files in this view.

The header icon opens Artifacts. Rename and delete use filesystem operations in
that bot directory. Existing target names are conflicts, not silent replacements.
Bot API routes keep the normal active-bot authorization and rooted path checks.
Only detected PNG, JPEG, GIF, and WebP bytes are inline; other files download.
Markdown is rendered as text by the client. HTML and SVG never run inline.
The open panel polls every three seconds, including in background tabs, and
updates after mutations and chat reconnect/completion. Selection uses the current
file name. Modification time refreshes previews when bytes change.

An agent calls `deliver_file` to copy a workspace-relative regular file of at most
20 MiB into its bot directory under the original name. Existing names fail with
a conflict. No deduplication or retry receipt is saved. Workspace source files
are not changed by artifact rename or delete. The tool returns the actual bot
file URL. A parent in another bot can link to that URL; it does not receive a
shared artifact entry or a second copy. Continue editing uses the current
writable conversation, or creates a Chat for a read-only conversation.

For a Telegram Main input, `deliver_file` also queues a native file for that input's
original recipient. The tool caller supplies the native execution, source receipt,
and operation key. A Web, child, or other channel call keeps URL-only delivery.
The channel path does not accept a model-supplied peer or bot identity.

The shared sender uses typed text and file parts. It freezes rendered parts before
the first send and stores confirmed remote message IDs with the part cursor in one
transaction. A restart skips confirmed parts. Delivery remains at least once: a
crash after Telegram accepts a request but before its receipt commits can repeat it.

`channel_reply_files` holds temporary byte snapshots for pending deliveries, with
at most ten files per input and 20 MiB per file. This table is not an artifact
index. It has no role in artifact listing, naming, rename, delete, or conflict checks.
A queued snapshot does not change when an artifact is renamed, deleted, or replaced.
SQLite removes the payload in the same transaction that completes, cancels, or
revokes its delivery. A missing payload records a permanent failure; the sender
never reopens an artifact with the same name.

Telegram sends valid PNG and JPEG results as photos when they meet the photo limits:
at most 10 MiB, width plus height at most 10,000, and aspect ratio at most 20.
Other files use documents. A definitive photo rejection can fall back to a document
with the same bytes. Network failures are retried without an immediate fallback.

Each Telegram input stores its authenticated sender and public bot account ID.
Child results copy that context with the original origin. The sender checks current
account ownership and the positive-integer allowlist before each final part.
Revocation discards pending payloads and remains in effect for those receipts if
the sender is later authorized again. Flood-control responses persist both the
account cooldown and reply retry time. API waits are cancellable and survive restart.

Telegram also accepts voice, audio, video, video notes, animations and stickers.
The adapter keeps the original upload bytes and records the media type. Video notes
remain video files. Animated stickers remain files with a type description.
Voice and audio input states that automatic transcription is not configured.
No speech provider, frame extractor, codec installer or cloud fallback runs automatically.
Location, contact and poll messages become text with their supplied fields.

Reply quotes and forward headers enter the ordinary model input as context, not as
caller identity. The outer authenticated sender keeps authority. The adapter does not
download quoted attachments. A pending Question keeps the user's answer text before
the existing question wrapper is added, so quote text cannot change an approval.
Forwarded text is not treated as a channel command.

Explicit file results use native MP3/M4A audio, Opus voice, MP4 video and GIF animation
methods when the detected container supports that route. A definitive media rejection
falls back to a document with the original bytes. The platform still checks the codec;
container routing does not prove that a real Telegram service can decode the file.

The existing Goldmark version parses GFM. Telegram rendering maps task checkboxes,
strikeout, numbered lists, blockquotes and code languages to supported text or HTML.
Tables use plain cell text in a preformatted block. Image links keep their label and
safe URL without fetching the image. Raw HTML remains escaped. The UTF-16 splitter
keeps valid tags across parts. An old partially delivered reply without a frozen
payload uses the original parser and rendering rules, so its cursor does not change.

Album members commit their upload references to `telegram_album_members` before
polling ACK. Collection closes after 800 ms without a new member, at three seconds,
or at ten files. The chat service freezes membership before it creates the normal
business input. Later members form an explicit continuation instead of changing a
submitted payload. A restart restores collection and sealed batches from SQLite;
raw member source keys resolve to the same stored input without downloading again.
Captions keep their text for Question matching. Album captions are input data, not
shared commands, even when a caption is `/clear` or `/stop`.

These member rows track collection and upload ownership, not native execution.
After a business receipt owns the files, their temporary input JSON is removed;
source aliases remain for replay detection. Native admission and reconciliation
still use the existing submission path. The receipt transaction checks that the
batch is still sealed. Stop, Clear, authorization revocation and bot archive can
discard unsubmitted batches without deleting files owned by an existing input.
An interrupted cleanup retries from the retained references. Rejected files use
the existing attachment error reply and do not enter the native album.

Consecutive compatible file results use one `sendMediaGroup` request with two to
ten members. Photos and videos can share a group. Audio and documents each use
their own group type. Voice and animation keep their single-file methods.
Membership and order freeze with the rendered reply. Existing frozen replies and
older partially delivered replies keep their original part boundaries.

A successful group confirms every remote message ID and the outer part cursor in
one transaction. A definitive API 400 rejection first commits fallback mode.
Fallback then uses native single-file methods and confirms each member in a
transaction. A restart skips confirmed members and cannot regroup the rejected
batch. The last member advances the outer cursor in the same transaction.
The sender checks current authorization before each fallback member.

Lost responses, invalid or incomplete message IDs, and service 5xx responses leave
the whole group unconfirmed. HTTP 5xx takes precedence over a contradictory error
code in the body. These outcomes never trigger fallback or confirm a prefix.
Retries use the same snapshots. Telegram has no delivery idempotency key here,
so remote acceptance before a failed response or local receipt can repeat an
unconfirmed group or member. Flood waits keep the existing durable account and
reply cooldowns. This is at-least-once delivery, not exactly-once delivery.

Presentation preferences and configured speech services remain open layer-2 work.
Group routing and isolated resources belong to layer 3; the adapter still rejects
non-private chats.

## Attachments

1. The browser sends each file as `POST /api/agents/{agentId}/uploads` (multipart, one file,
   at most 20 MB). [`upload.Store`](../../internal/upload/upload.go) writes
   it to `workspace/uploads/<UUIDv7>/<name>` and returns its ID, name, size,
   type (from the content) and workspace path. The directory is the only
   record of an upload.
2. A message carries at most 10 upload IDs. Control checks each ID and stores
   the attachments as JSON with the submission, so a replay sends the same
   list.
3. The engine gives the model the text and then `Attached files:` with one
   workspace path per line. The model reads a file with the read tool, which
   also prepares images for a model with image input. The `dex.user` entry
   keeps the attachments, so the transcript shows the files and the text
   without the list.
4. `GET /api/agents/{agentId}/uploads/{id}` serves a file for the transcript.
