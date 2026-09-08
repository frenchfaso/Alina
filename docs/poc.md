# Running the POC

Build with Go 1.26 or newer. No Node.js, Python or Lua runtime is required by
Alina. External commands remain the user's installed programs.

```sh
go build -trimpath -o alina .
./alina setup
./alina serve
```

`setup` configures the default provider/model, working directory, all search
providers, and Telegram. It does not install system packages or enable services.
Secrets are hidden in interactive terminals and saved with file mode `0600`.
Files are private plaintext, not encrypted. Configuration updates are atomic.
Run setup/login with the service stopped, then restart the service.

## Providers

- **ChatGPT Plus/Pro:** `alina login` runs a dedicated OAuth device login.
  Enable device login in ChatGPT security settings if required. The fallback
  `alina login browser` uses PKCE and validates OAuth state. Its callback binds
  only `127.0.0.1:1455`; on a remote device you can paste the complete callback
  URL after authenticating. Credentials are stored in `chatgpt.json`; Alina
  refreshes its own token and never imports another application's login.
  The subscription adapter follows the Codex-compatible protocol used by Pi;
  this is not a guarantee that a private backend will remain compatible.
- **OpenCode Go:** enter the subscription API key and a model ID in setup.
  Select `chat`, `responses`, or `messages` according to that model's documented
  endpoint. Alina identifies itself with its own user agent and a stable
  `x-opencode-session`. The initial example is `glm-5.1` / `chat`; availability
  depends on the account and current provider catalog. No automatic paid fallback.

`alina doctor` checks configuration without disclosing credentials.
`alina doctor --live` sends a small model request, tests each configured search
provider and checks the Telegram bot with `getMe`. Live checks consume the
respective provider's normal usage. It does not send Telegram messages.

## Search

`web_search` accepts `openai`, `tavily`, or `brave`; an omitted provider uses the
configured default. Results include source URLs and bounded snippets. It does
not save arbitrary remote files or download Telegram attachments.

OpenAI search uses Responses `web_search`. With an explicit OpenAI API key it
uses the public API and **separate API billing**. Without that key it tries the
ChatGPT Codex backend, whose hosted search availability depends on the backend,
account and model. An unavailable search returns an error; it never silently
switches to API billing. Tavily and Brave each require their own API key.

## Terminal and Telegram

```sh
alina chat                        # plain text, no full-screen TUI
alina ask "Show free disk space"
alina ask -session maintenance -detach "Inspect recent logs"
alina job JOB_ID
alina status
alina cancel JOB_ID
cat error.log | alina ask "Explain this log"
```

The terminal client talks HTTP/JSON over a private Unix socket. For example:

```sh
curl --unix-socket "$HOME/.config/alina/alina.sock" http://alina/v1/status
```

`chat` supports `/new`, `/status`, `/permissions`, `/revoke ID`, and `/quit`.
Ctrl-C cancels the current job and exits the terminal client. A detached job
continues without a client. Piped requests that need approval leave the job
pending and print an `alina approve` command; EOF never means approval.

Telegram uses Bot API long polling. Setup explains `/newbot` in BotFather,
verifies the token using `getMe`, then offers pairing: open its generated link
and press Start in a private chat. A fresh random code identifies that chat;
other users and groups cannot claim ownership without it. Pairing reads updates
but sends no messages. Manual numeric owner ID entry is also available. Use a
dedicated bot without another poller or webhook. Only messages
and buttons from that owner in a private chat are accepted. Groups and other
users are ignored. Commands: `/status`, `/cancel ID`, `/new`, `/permissions`,
`/revoke ID`. Attachments are deliberately not fetched in this POC.

## Approvals

Both clients offer:

1. **Solo una volta** — only the pending invocation.
2. **Fino al riavvio di Alina** — an in-memory grant for identical operations.
3. **Fino a revoca** — a persisted grant, removed with `alina revoke ID`.
4. **Nega** — returns denial to the agent without executing the command.

Grants match the exact shell command text, absolute working directory, and
network access flag. They do not grant a whole category such as all downloads.
They are shared between the owner's local and Telegram interfaces. Changed
arguments or directories require a new grant. A reusable command containing
variables or invoking a mutable script still has that command's dynamic
meaning; grants do not freeze script contents. Pending requests expire after
15 minutes and cannot be replayed by reusing an old approval ID.

`alina permissions` lists reusable grants. Revocation prevents future reuse;
use `alina cancel JOB_ID` to stop an already-running operation. A service restart
clears restart-scoped grants and marks unfinished jobs interrupted, without
replaying commands. A crash before the final result is saved can leave an
operation's outcome unknown.

## Execution limits

- Ordinary local shell operations run without confirmation. Known package
  manager actions and shell network commands request consent; the model can
  explicitly request network access too. Approval permits that shell invocation
  to use the network, including redirects and subprocesses.
- On **Linux/Android ARM64 and AMD64**, unapproved shell processes receive an
  inherited seccomp filter denying socket creation/connections (including Unix
  sockets), ptrace, cross-process memory writes and io_uring creation. Existing
  socket descriptors are not passed to the child. Filter installation failures
  stop execution. This also blocks network calls made inside scripts.
- On **macOS**, the POC uses the system `sandbox-exec` network denial profile.
  This deprecated OS facility is a portability risk. On systems without a
  supported filter, including the current **BSD** implementation, shell execution
  requires approval instead of silently running unrestricted.
- **This is a trusted-owner POC, not a complete sandbox.** The shell shares the
  service's OS user and can modify that user's files. Offline installation,
  indirect package-manager execution, malicious scripts and tampering with Alina
  state are not comprehensively isolated. Package classification is conservative
  and is not a security boundary. Strong enforcement would require separating
  privileges/filesystem access as well. Alina's prompt forbids bypassing policy
  or reading/modifying its credentials and grant store.
- Child environments exclude API keys and other arbitrary daemon environment
  variables. Shell output is capped at 48 KiB, command execution at 120 seconds
  by default. Process groups are killed on cancellation/timeout and after their
  foreground command exits. Deliberately daemonized descendants are outside the
  POC's process-group guarantees.
- One agent turn runs at a time; up to 16 requests may be active/queued. Commands
  in different jobs do not share a persistent shell. Jobs and sessions use atomic
  JSON files. Context is bounded at 200 stored messages / 512 KiB before a new
  turn; automatic session compaction is not implemented. Daily memory
  consolidation is a separate process, described below.
- Model results are displayed when a turn completes; the client polls activity
  and approval state. The Responses adapter parses SSE, but token-by-token chat
  display is not implemented. Telegram response delivery is best-effort retry;
  a crash between send and checkpoint can duplicate a notification. Incoming
  update IDs deduplicate submitted jobs.

## Termux service

The installer uses an already-installed `termux-services` and creates only an
`alina` service; it refuses to overwrite an existing one. Run it after setup:

```sh
sh scripts/install-termux-service.sh /absolute/path/to/alina
sv up alina
sv down alina
```

Logs: `$PREFIX/var/log/sv/alina/current`. The service stays disabled until
explicitly started; use `sv-enable alina` to enable it. Termux:Boot can start the
service supervisor at boot. Android Doze, battery restrictions and process
limits still apply; a wake lock alone does not guarantee availability. This
POC does not change Android battery settings or other services.

On Linux/macOS/BSD, run `alina serve` under the native user-service supervisor.
Alina does not fork itself into the background or require root.

## Memory, soul and dream

Setup enables memory and configures the timezone (IANA name such as
`Europe/Rome`, or the process-local timezone), optional location, dream schedule
and optional embedding endpoint. Location is entered by the user; Alina does
not infer it from IP or acquire GPS data.

- `memory/today.md`: detailed current-day user messages, assistant text, tool
  requests/results and explicit notes. No hidden model reasoning is recorded.
- `memory/week.md`: one Markdown view of the preceding seven calendar dates,
  containing important daily summaries. Unprocessed days are explicitly marked.
- `memory/memory.sqlite`: journal for reliable recovery, consolidated days,
  long-term nuclei, source metadata, embeddings and soul revision history.
- `soul.md`: Alina's short personal orientation, up to 180 words / 1600 bytes.
  You can edit it while the service is stopped. Dream saves previous and proposed
  versions plus its reason in `soul_versions` before replacing the file.

The Markdown memory files are generated views; editing them does not edit the
database. Use the agent's `memory` note action to add information. Recent memory
is supplied as historical data, separately from the minimal system instructions,
host metadata and soul. It receives at most 12 KB for the week and 16 KB for the
current day; other records can be requested with `memory`.

Dream defaults to `0 3 * * *`, with a single catch-up after missed executions.
It consolidates closed days in bounded chunks, validates source IDs, then archives
dates **older than today minus seven calendar days**. Archiving inserts nuclei
and source metadata and removes that day's detailed journal in one SQLite
transaction. Existing days and chunks are reused after interruption. Invalid
summaries leave their originals intact. A later soul/embedding failure does not
roll back already completed consolidation or archive transactions.

The same configured model handles consolidation and reflection, without tools.
Dream can keep the soul unchanged and skips reflection without new recent
activity. It is capped at 32 model calls and ten minutes per invocation; work
beyond that budget is retained for another invocation. An ordinary day generally
needs one consolidation call and one reflection call. Model judgment, including
the truth or usefulness of a summary, is not proven by schema validation.

Embeddings are optional and use an explicitly configured OpenAI-compatible
endpoint (HTTPS, or HTTP on loopback). Only nuclei and search queries are sent to
that endpoint. OpenAI API embeddings have separate billing from ChatGPT; a local
server can be configured instead. Vectors live in SQLite and Go computes exact
cosine similarity with a small text relevance contribution. This avoids a native
vector extension for the POC, but search is linear in archive size. Different
endpoint/model identities are never compared; reindex after changing them.
Dimension mismatches are ignored. Without embeddings or after provider failure,
results clearly report text search. Dream indexes up to 100 pending nuclei.

```sh
alina dream
alina memory read today
alina memory read week
alina memory read soul
alina memory read 2026-09-01
alina memory search "What did we decide about backups?"
alina memory reindex
```

All storage is private plaintext. Known configured keys and common token patterns
are redacted in memory, but this cannot recognize every possible secret. Ordinary
job/session logs are separate and have no automatic retention policy yet; memory
consolidation is not a complete data-erasure operation. Source metadata points to
the original session/job, while the archive intentionally omits old raw journal
content. Corrections are stored as new dated evidence, not automatic destructive
rewrites of old nuclei.

## Portable recurring tasks

The scheduler runs in the daemon; it does not require a system cron daemon.
Schedules persist in `tasks.json` and use five-field cron or descriptors such as
`@every 6h` (minimum interval one minute). A task is an agent request, subject to
the same shell permissions as interactive work. Each occurrence has a fresh
session and stable job ID; restarts do not replay a submitted occurrence.

```sh
alina tasks
alina tasks add "Disk check" "0 9 * * *" "Check local free disk space"
alina tasks pause TASK_ID
alina tasks resume TASK_ID
alina tasks remove TASK_ID
```

The agent can manage these through the `schedule` tool when requested. Catch-up
coalesces missed occurrences to one execution; it never replays every missed
tick. Executions of the same task do not overlap. The terminal command defaults
to catch-up; the tool can disable it. Scheduled Telegram tasks inherit the chat
owner and deliver results/approval requests there. System dream jobs are visible
through `alina status`; they do not send routine Telegram notifications.

All work shares one FIFO worker, so a running dream or pending approval can delay
other requests. Use `alina cancel` if needed. Android suspension still suspends
the daemon: the scheduler is portable, not an Android exact-alarm service.
