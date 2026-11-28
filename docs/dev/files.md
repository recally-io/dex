# Files and attachments

This document describes the bot artifacts directory, file results, channel
file delivery, and attachments.

## Artifacts and file delivery

Each Bot lists the regular files directly under `<data dir>/agents/<bot ID>/artifacts/`.
The list uses file names, sizes, and modification times. It excludes links
and subdirectories.

The header icon opens Artifacts. Rename and delete use filesystem operations in
that bot directory. Existing target names are conflicts, not silent replacements.
Bot API routes keep the normal active-bot authorization and rooted path checks.
Only detected PNG, JPEG, GIF, and WebP bytes are inline; other files download.
Markdown is rendered as text by the client. HTML and SVG never run inline.
The open panel polls every three seconds, including in background tabs, and
updates after mutations and session reconnect/completion. Selection uses the current
file name. Modification time refreshes previews when bytes change.

A bot calls `deliver_file` to copy a workspace-relative regular file of at most
20 MiB into its bot directory under the original name. Existing names fail with
a conflict. No deduplication or retry receipt is saved. Workspace source files
are not changed by artifact rename or delete. The tool returns the actual bot
file URL. A parent in another bot can link to that URL; it does not receive a
shared artifact entry or a second copy. Continue editing uses the current
writable conversation, or creates a Session for a read-only conversation.

### Telegram

For a Telegram Main input, `deliver_file` also queues a native file for that input's
original recipient. The tool caller supplies the native execution, source receipt,
and operation key. A Web, child, or other channel call keeps URL-only delivery.
The channel path does not accept a model-supplied peer or bot identity.

Each file has its own Outbox row and one platform receipt. A restart does not send a confirmed file again. A crash after Telegram accepts the file but before the receipt commits can repeat the file.

`outbox_files` holds the temporary byte snapshots, with at most ten files per input and 20 MiB per file. A snapshot does not change when an artifact is renamed, deleted, or replaced.
SQLite removes the payload in the same transaction that completes, cancels, or
revokes its delivery. A missing payload records a permanent failure; the sender
never reopens an artifact with the same name.

Telegram sends valid PNG and JPEG results as photos when they meet the photo limits:
at most 10 MiB, width plus height at most 10,000, and aspect ratio at most 20.
Other files use documents. A definitive photo rejection can fall back to a document
with the same bytes. Network failures are retried without an immediate fallback.

Each Telegram input stores the sender and public bot account ID. Unpairing fails pending deliveries and removes their byte snapshots. A later pairing does not revive the failed deliveries. Flood control keeps the account cooldown and Outbox retry time across restart.

See [Telegram input](channels.md#telegram) for accepted media, quotes, and
optional audio transcription.

Explicit file results use native MP3/M4A audio, Opus voice, MP4 video and GIF animation
methods when the detected container supports that route. A definitive media rejection
falls back to a document with the original bytes. The platform still checks the codec;
container routing does not prove that a real Telegram service can decode the file.

Album members commit their upload references to `telegram_album_members` before
polling ACK. Collection closes after 800 ms without a new member, at three seconds,
or at ten files. The session service freezes membership before it creates the normal
business input. Later members form an explicit continuation instead of changing a
submitted payload. A restart restores collection and sealed batches from SQLite;
raw member source keys resolve to the same stored input without downloading again.
Captions keep their text for Question matching. Album captions are input data, not
shared commands, even when a caption is `/clear` or `/stop`.

These rows track collection and upload ownership.
After a business receipt owns the files, their temporary input JSON is removed;
source aliases remain for replay detection. Native admission and reconciliation
still use the existing submission path. The receipt transaction checks that the
batch is still sealed. Stop, Clear, authorization revocation and bot archive can
discard unsubmitted batches without deleting files owned by an existing input.
An interrupted cleanup retries from the retained references. Rejected files use
the existing attachment error reply and do not enter the native album.

## Attachments

1. The browser sends each file as `POST /api/bots/{botId}/uploads` (multipart, one file,
   at most 20 MiB). [`upload.Store`](../../internal/upload/upload.go) writes
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
4. `GET /api/bots/{botId}/uploads/{uploadId}` serves the file. Only PNG,
   JPEG, GIF, and WebP are inline. Other files use `application/octet-stream`
   and `Content-Disposition: attachment`. Responses set `nosniff`.

Upload IDs must be canonical UUIDs. Names cannot contain path separators or
control characters and may use at most 255 bytes. Removing a composer
attachment does not delete the upload. Bot tools can change or delete files
in the workspace; the transcript can then show changed content or a broken link.
