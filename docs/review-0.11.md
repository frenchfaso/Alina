# 0.11: Telegram conversation essentials

Telegram now shows the native typing indicator while a user chat runs. The
worker sleeps on the existing job signal when idle and refreshes every four
seconds only while needed. Errors back off to thirty seconds. Dreams, personal
initiatives and consent waits do not emit typing; no progress messages or
answer streaming were added.

Successful replies use CommonMark (Goldmark) projected into Telegram entities:
bold, italic, links and code. UTF-16 offsets and message limits account for emoji;
long replies split with valid per-message spans and prefer newline boundaries.
If Telegram rejects formatted text, retry that chunk as plain text. Consent
commands and diagnostics stay literal text.

A native Telegram reply contributes the quoted text/caption, capped at 8 KB,
as explicitly labelled user-supplied context. It must belong to the same private
chat. This does not query another family's archive or fetch quoted attachments.

The send_file tool appears only for Telegram user delivery contexts. It queues
up to four workspace files of at most 20 MiB each. A private outbox snapshot and
hash preserve each original through subsequent edits and restarts. Files go to
the current user as documents; no model-selected recipient or remote URL is
accepted. Personal exploration and global reflection cannot use the tool.
Cancelled or failed jobs do not send their queued files. Text chunks and uploads
have individual SQLite receipts, so retrying an upload does not repeat already
confirmed parts. A crash between Telegram acceptance and receipt persistence
can still duplicate a part. Snapshots remain in the workspace for inspection.

Setup recovery: deferring a failed Telegram health check preserves the previous
enabled state. Setup offers to reactivate stored disabled credentials; unchanged
bots with an existing owner no longer require pairing just because they were
disabled. Explicit disable remains available in advanced setup/config.

The Galaxy's saved configuration had enabled=false with its token and two family
members intact. getMe and getWebhookInfo succeeded. Only enabled was restored,
under the daemon lock, with a private configuration backup. No real messages
were sent as part of the repair or tests.

Validation: macOS race suite and Go vet; targeted tests cover formatting/emoji,
plain fallback, quote boundaries, typing, setup recovery, snapshot integrity,
partial-delivery retries and family/tool visibility. Android validation and the
live setup check are recorded in docs/verification.md.

Sources consulted:
- https://docs.openclaw.ai/channels/telegram/messaging
- https://docs.openclaw.ai/concepts/typing-indicators
- https://github.com/openclaw/openclaw/blob/main/extensions/telegram/src/bot/delivery.replies.ts
- https://core.telegram.org/bots/api
