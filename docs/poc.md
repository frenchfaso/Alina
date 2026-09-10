# Running the POC

Build with Go 1.26 or newer. No Node.js, Python or Lua runtime is required by
Alina. External commands remain the user's installed programs.

```sh
go build -trimpath -o alina .
./alina setup
```

`setup` connects ChatGPT, pairs Telegram and checks the model and the selected
search provider. The default path needs only the ChatGPT device login, a dedicated
Telegram bot token and a tap on its pairing link. An existing login and verified
bot are reused. It then offers to start Alina in the background. Use Telegram or `alina chat`;
`alina serve stop` stops the daemon. Setup does not install packages or register
a boot/login service.

New quick setups enable memory, the 03:00 dream, catch-up, personal exploration
(12 model calls/day, five minutes/run, with search), and the declared network
policy. Astra's existing model, reasoning and context defaults are retained.
The host timezone is detected, including Android's system timezone. Repeating
setup preserves existing custom settings and explicitly disabled features.

`alina setup --advanced` retains provider/model selection, alternative search
keys, work directory, memory/embedding settings and budgets. Run `alina setup`
afterwards for connection checks. `alina setup telegram` pairs or replaces the bot
and manages people in the shared family, preserving the other settings.
`alina setup --no-start` configures without offering to launch; piped
input never starts the daemon. Setup/login require the service to be stopped.

Connection checks make a small model request and, when enabled, a search request
using the configured accounts. They send no Telegram messages. Failures can be
retried; account choices are saved before model/search checks so the setup can
resume. Deferring Telegram or search is explicit and reported at the end.
Optional embedding endpoints and document converters remain advanced options;
quick setup does not install MarkItDown or claim those integrations were tested.

Secrets are hidden in interactive terminals and saved with file mode `0600`.
Files are private plaintext, not encrypted. Configuration updates are atomic.

## Providers

- **ChatGPT Plus/Pro:** `alina setup login` runs a dedicated OAuth device login.
  The default model is `gpt-6-astra`, with a 272,000-token working window.
  Chat uses medium reasoning, dream uses high, checkpoints and hosted search
  use low. See [Astra defaults and verification](astra.md).
  Enable device login in ChatGPT security settings if required. The fallback
  `alina setup login browser` uses PKCE and validates OAuth state. Its callback binds
  only `127.0.0.1:1455`; on a remote device you can paste the complete callback
  URL after authenticating. Credentials are stored in `chatgpt.json`; Alina
  refreshes its own token and never imports another application's login.
  The subscription adapter follows the Codex-compatible protocol used by Pi;
  this is not a guarantee that a private backend will remain compatible.
- **OpenCode Go:** enter the subscription API key and a model ID in `alina setup --advanced`.
  Select `chat`, `responses`, or `messages` according to that model's documented
  endpoint. Alina identifies itself with its own user agent and a stable
  `x-opencode-session`. The initial example is `glm-5.1` / `chat`; availability
  depends on the account and current provider catalog. No automatic paid fallback.

`alina doctor` returns JSON diagnostics without disclosing credentials. See
[the minimal CLI and operational logs](operations.md) for configuration, debugging and repairs.
`alina doctor --live` sends a small model request, tests the selected search
provider and checks the Telegram bot with `getMe`. Live checks consume the
respective provider's normal usage. It does not send Telegram messages.

## Search

`web_search` accepts `openai`, `tavily`, or `brave`; an omitted provider uses the
configured default. Results include source URLs and bounded snippets. It does
not save arbitrary remote files. Telegram uploads use the separate inbox below.

OpenAI is the default search provider and reuses Alina's dedicated ChatGPT login.
An empty `search.openai_model` follows the main ChatGPT model (Astra if the main
provider is OpenCode Go). No second key is required for the subscription route.
OpenAI search uses Responses `web_search`. With an explicit OpenAI API key it
uses the public API and **separate API billing**. Without that key it tries the
ChatGPT Codex backend, whose hosted search availability depends on the backend,
account and model. An unavailable search returns an error; it never silently
switches to API billing. Tavily and Brave each require their own API key.

## Terminal and Telegram

```sh
alina chat
alina chat "Show free disk space"
alina status
alina status JOB_ID
alina api POST /v1/jobs/JOB_ID/cancel
cat error.log | alina chat "Explain this log"
```

The terminal client talks HTTP/JSON over a private Unix socket. `alina api`
exposes the same endpoints without requiring a platform-specific socket path.
See [state locations](operations.md#state-directory) when selecting an instance.

`alina api POST /v1/jobs/JOB_ID/resume` resumes interrupted/failed work in its original session,
with an explicit instruction to check current state before repeating effects.
`alina api GET /v1/intentions` lists Alina's personal questions and projects.

`chat` stays usable while Alina works: new lines steer the active chat job.
It supports `/new`, `/status`, `/permissions`, `/revoke ID`, `/cancel ID`,
`/approve 1|2|3|4`, and `/quit`. `alina api POST /v1/jobs/ID/steer` with a JSON message works from another
terminal. Telegram text and attachments also steer active chat work.
Ctrl-C cancels followed jobs and exits the terminal client; `/quit` detaches.
See [web reading, steering and optional document conversion](web-steering.md). A detached job
continues without a client. Piped requests that need approval leave the job
pending and print an `alina api` approval command; EOF never means approval.

Telegram uses Bot API long polling. Setup explains `/newbot` in BotFather,
verifies the token using `getMe`, then pairs each person: open its generated link
and press Start in a private chat. A fresh random code identifies that chat;
other users and groups cannot claim membership without it. Pairing reads updates
but sends no messages. Messages from already authorized people are preserved in
a durable inbox. Changing the bot isolates its update IDs and delivery receipts;
adding or editing people preserves that binding. Use a dedicated bot without
another poller or webhook. Only messages and buttons from configured people in
private chats are accepted. Groups and unconfigured users are ignored. See
[native families and global introspection](people.md). The Telegram menu exposes `/model`, `/think`, `/status`, `/stop`
and `/help`. `/new`, `/permissions`, `/revoke ID`, `/cancel ID` and
`/intentions` remain available outside the short menu. See
[personal model controls](operations.md#telegram-model-and-reasoning-controls).

With memory enabled, Alina records reaction changes from configured people in
their private chats as feedback: who reacted, the previous/new reactions and an
excerpt of the original message. They enter the family's searchable archive and
subsequent context/dream; they do not start or steer work, send acknowledgements,
approve operations or automatically become lasting preferences. Removed reactions
are recorded too. Custom emoji retain their Telegram ID, not an invented meaning.
Only messages whose references were saved by this version can be linked; older
or unknown messages are ignored. Telegram supplies the message ID, not its text.
See [Bot API reaction updates](https://core.telegram.org/bots/api#messagereactionupdated).

Each configured person can send photos or files, with an optional caption.
Alina downloads that specific upload into their scope's `workspace/inbox/telegram/`, with private
permissions and a generated filename. Sending a file authorizes receiving it;
URLs in captions and arbitrary remote downloads still follow the usual policy.
The original name, detected MIME type, size, hash and local path accompany the
message. Telegram update IDs prevent repeated work; completed downloads are
reused on retries and existing originals are never overwritten.

Photos use the largest Telegram representation. PNG, JPEG and WebP images,
including images sent as documents, are delivered directly to Astra through
Responses image input. `view_image` reopens saved workspace images, including
ones recovered through memory after a context checkpoint or restart. Image bytes
are read only for model requests; journals and job records store paths and hashes.
At most four recent images and 20 MiB total image data enter each request. Older,
missing or modified images have an explicit text notice; their paths remain
available. Reopening an image refreshes its place in the visual context. Changes
to the set of included images can reduce prompt-cache reuse.

Other files (PDFs, text, archives, audio and video) are available to installed
shell tools. Receiving them does not execute them or install dependencies. There
is no built-in audio transcription or video understanding. OpenCode's `chat` and
`messages` adapters currently supply file metadata only and explicitly report the
visual limitation; direct vision requires a compatible model using `responses`.

Each file is capped at 20 MiB, in line with the hosted Bot API's download limit;
the server can also reject an upload. Interrupted downloads retry; oversized or
unavailable files produce a message asking the owner to resend. Album items are
processed as separate ordered messages. Files persist until explicitly removed;
there is no automatic inbox expiry. A crash before saving the completed receipt
can leave an unreferenced file; retrying uses a fresh filename. Telegram photo mode can compress images;
send an image as a file when preserving the original matters. See the
[Telegram getFile reference](https://core.telegram.org/bots/api#getfile) and
[Responses image input](https://developers.openai.com/api/docs/guides/images-vision).

## Native file tools

The ordinary agent loop exposes `shell`, `read`, `write`, `edit`, `web_search`,
`web_fetch`, `memory`, `schedule`, `view_image` and `harness`; Telegram user turns
also expose `send_file`. The file tools follow Pi's
small interfaces, implemented directly in Go without additional dependencies:

Disabled search/memory and unsupported image adapters are omitted from the tool
set. The prompt follows that set and dispatch checks it before executing calls.


- `read(path, offset?, limit?)`: UTF-8 text, with one-based line offsets and
  `next_offset` for continuation. A page contains at most 2000 whole lines or
  32 KiB; reads stream instead of loading an entire file. Scanning is capped at
  32 MiB per call. Very long lines and other formats can use bounded shell tools.
- `write(path, content)`: create or fully rewrite a UTF-8 file up to 8 MiB,
  creating parent directories. Empty content is allowed only when explicitly
  supplied. New files are private (0600).
- `edit(path, edits)`: one to 32 `{oldText, newText}` replacements, each unique
  and disjoint in the original file. The whole operation is validated before
  writing. Missing, repeated or overlapping matches leave the file unchanged.
  Matching is exact, including whitespace and line endings; unchanged bytes,
  including BOM and CRLF, remain intact. Empty `newText` deletes the matched text.

Write/edit use a temporary file in the same directory followed by rename,
preserve existing permission bits, and serialize native mutations in the daemon.
They check for external changes before committing and abort if detected. This
does not lock out shell commands or other processes; a final race is still
possible. Atomic replacement changes the inode and does not preserve hard-link
relationships, extended attributes or arbitrary ownership metadata.

Relative paths use the configured workdir; `~/` and absolute paths are accepted.
Personal initiatives resolve relative paths inside the workspace and reject
resolved paths outside it. Alina's administrative state is excluded; archived
session files are readable, and managed state uses its dedicated tools. Existing
symlinks resolve to their targets, with the same path checks; a write preserves
the symlink itself. These are convenience guards under the existing trusted-owner
model, not a complete filesystem sandbox. Local file tools need no new consent.

The English system prompt briefly explains when to use each tool; argument and
limit details live in tool descriptions. Reflection exposes memory, schedule,
image inspection (when supported), read-only harness access and soul revision.

## Approvals

Both clients offer:

1. **Solo una volta** — only the pending invocation.
2. **Fino al riavvio di Alina** — an in-memory grant for identical operations.
3. **Fino a revoca** — a persisted grant, removed with `alina api DELETE /v1/grants/ID`.
4. **Nega** — returns denial to the agent without executing the command.

Grants match the exact shell command text, absolute working directory, and
network access flag. They do not grant a whole category such as all downloads.
They are shared between local and Telegram interfaces in the same memory scope;
a pending Telegram approval belongs to its requester. Changed
arguments or directories require a new grant. A reusable command containing
variables or invoking a mutable script still has that command's dynamic
meaning; grants do not freeze script contents. Pending requests expire after
15 minutes and cannot be replayed by reusing an old approval ID.

`alina api GET /v1/grants` lists reusable grants. Revocation prevents future reuse;
use `alina api POST /v1/jobs/JOB_ID/cancel` to stop an already-running operation. A service restart
clears restart-scoped grants and marks unfinished jobs interrupted, without
replaying commands. A crash before the final result is saved can leave an
operation's outcome unknown.

## Execution limits

- **declared** network policy asks for declared or recognized arbitrary file
  downloads and package changes. Plain network use, such as querying an endpoint,
  can proceed with `network=true`. The model must declare `download=true` or
  `install=true` for indirect operations too. This is a trusted-agent contract:
  shell text classification does not enforce the difference between a query and
  a download. New interactive setups suggest this policy.
- **strict** policy asks for any network access from the shell and uses the OS
  network filter below. Existing configurations retain strict policy until changed
  in setup. The three approval lifetimes still apply to exact commands/directories.
- Local `npm run`/`go run` are no longer classified as installation solely because
  of `run`; commands that actually need to fetch dependencies must declare it.
- On **Linux/Android ARM64 and AMD64**, unapproved shell processes receive an
  inherited seccomp filter denying non-Unix socket creation, all outbound
  connections (including Unix sockets), ptrace, cross-process memory writes and
  io_uring creation. Local Unix listeners and socketpairs are allowed: Termux:API
  connects back to such listeners to return device results. Existing socket
  descriptors are not passed to the child. Filter installation failures stop
  execution. Direct TCP/UDP calls made inside scripts remain blocked. Device
  APIs and other processes reached through local IPC have their own privileges;
  this filter is not an isolation boundary against delegated operations.
- On **macOS**, the POC uses the system `sandbox-exec` network denial profile.
  This deprecated OS facility is a portability risk. On systems without a
  supported filter, including the current **BSD** implementation, strict-policy shell execution
  requires approval instead of silently running unrestricted.
- **This is a trusted-owner POC, not a complete sandbox.** The shell shares the
  service's OS user and can modify that user's files. Offline installation,
  indirect package-manager execution, malicious scripts and tampering with Alina
  state are not comprehensively isolated. Package classification is conservative
  and is not a security boundary. Strong enforcement would require separating
  privileges/filesystem access as well. Alina's prompt forbids bypassing policy
  or reading/modifying its credentials and grant store.
- Child environments exclude API keys and other arbitrary daemon environment
  variables, while retaining Android runtime paths and boot classpaths required
  by Termux:API's `am`/`app_process`. Shell output is capped at 48 KiB, command execution at 120 seconds
  by default. Process groups are killed on cancellation/timeout and after their
  foreground command exits. Deliberately daemonized descendants are outside the
  POC's process-group guarantees.
- Up to 16 jobs per memory scope can be active. Each session processes messages in order;
  independent sessions can progress while another waits for approval or a shell
  process. One model request runs at a time, with foreground requests ahead of
  waiting dream/initiative calls. An in-flight model request is not preempted.
  Use a separate session (`POST /v1/jobs` with a chosen session, or `/new`) for an independent request
  while the current conversation is waiting for consent.
- Jobs are persisted in SQLite; status loads only active and 50 recent finished
  jobs. Old `jobs/*.json` are imported without overwriting existing IDs and retained
  as migration backups. Specific older jobs remain available by ID. Older binaries
  may not understand the current state schema; a rollback requires a matching
  backup of the state. Status `completed` means the turn finished; it is not
  independent proof of task success.
- Working context is compacted at model-call boundaries when its size,
  including prompts and tool schemas, exceeds 95% of `context_tokens` (default
  272000 for Astra: the threshold is 258400). The most recent provider input/output
  usage anchors the count, plus estimates for new messages and changed prefix
  overhead. Without usage, Alina estimates visible serialized bytes / 3;
  opaque encrypted reasoning and internal metadata are not tokenized as text.
  Unmeasured image input adds an estimated 12000 tokens per image, capped at four.
  This is not an exact preflight tokenizer. Configure the budget for the selected
  model; personal model selections can further cap it using catalog metadata.
  Message count and elapsed days
  do not trigger compaction. Checkpoints preserve objectives, verified results,
  uncertainty and next actions. Whole tool exchanges stay together. The shared
  SQLite archive and earlier JSON transcripts remain intact on success or failure.
- Context snapshots (time, current source, intentions and recalled notes) are
  appended at the start of each new turn; steering messages join that turn. Earlier snapshots stay unchanged until
  compaction, allowing the previous request prefix to be reused by the provider.
  They are not inserted into the event archive as new observations. Stable
  instructions include the host, soul and configured permissions.
- Responses usage is saved with each response and aggregated in job `usage` for
  the agent loop and checkpoints. Hosted OpenAI search uses a separate request but shares the job budget,
  model gate and usage aggregate. The counters describe tokens, not Plus/Pro quota
  percentages. Model HTTP timeout is 600 seconds; auth, Telegram and other HTTP
  clients retain their separate timeout. Job cancellation and job time budgets
  still take precedence.
- Model results are displayed when a turn completes; the client polls activity
  and approval state. The Responses adapter parses SSE, but token-by-token chat
  display is not implemented. Telegram response delivery is best-effort retry;
  a crash between send and checkpoint can duplicate a notification. Incoming
  update IDs deduplicate submitted jobs and steering messages.

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

On all supported Unix targets, `alina serve` starts in the background without root.
Use `alina serve --foreground` under a native service supervisor. Boot/login
autostart remains an explicit supervisor configuration.

## Shared memory, attention and reflection

Alina shares memory across the family’s channels and her own reflections. Source,
job, role and timestamp are provenance and reply-routing information, not recall
boundaries. Each working conversation still orders its tool exchanges and keeps
its immediate task context; a bounded excerpt from other conversations helps
continuity across channels. `/new` changes working context without erasing memory.
One dream can update the shared soul; its history and complete traces are
accessible to the family. Existing archives remain in place, including legacy
multiple-family boundaries. See [people and one global mind](people.md).

There are three independent concerns:

- **Archive:** SQLite records messages, tool requests/results and notes immediately.
  FTS5 searches recorded events and notes across all dates and sources. Read by ID,
  date, `archive`, or `after-N` (SQLite event sequence); long reads return
  `next_offset`. Search returns excerpts with provenance; read the source for detail.
  Events are not rewritten or deleted by age, dream or context compaction.
- **Attention:** a bounded selection of notes about facts, preferences, lessons and
  hypotheses. Unpinned attention halves every 30 days since creation or deliberate
  recall. This is a tunable design assumption in code, not a biological model or
  expiry date. Recent notes take precedence as new material competes for the
  6000-byte focus budget. Up to 3000 bytes including overhead can be pinned.
  `memory read ID`, `focus`, or re-saving the identical current note renews attention.
  Merely searching or injecting a note into the prompt does not reinforce it.
  Attention is not confidence: corrections always supersede older notes.
- **Working context:** the finite material for the current model request. Its
  automatic checkpointing responds to context pressure, independently of dream.
  Nothing is promoted to permanent belief merely because a checkpoint mentions it.

`memory/focus.md` is a generated view of attention, not an independent source of
truth. Edit notes through tools; `note` supports optional evidence `sources` and
an explicit `supersedes` ID. Unknown sources are rejected. Superseded records leave ordinary retrieval, but remain readable by ID.
Citing a superseded record does not implicitly supersede the citing note.
A source citation establishes provenance, not that an interpretation is correct.

`soul.md` remains a short personal orientation (180 words / 1600 bytes), with a
last-valid fallback and revision history. A reflection uses the `soul` tool with
its exact previous text and a reason; a concurrent file edit prevents replacement.
It can finish naturally without changing the soul or creating any notes.
Internal writing is English: soul, notes, intentions, procedures, checkpoints,
search notes and reflections. User-facing model replies use the user's language;
original messages and evidence retain their original language. Version 0.4
translates the untouched Italian seed soul with a revision record; personal edits
are preserved and the reflection prompt guides faithful translation when needed.

Dream defaults to `0 3 * * *`, with one catch-up after missed executions. It uses
**the same agent loop and imprinting as chat**, with memory, scheduling, saved-image
inspection, harness inspection and soul revision tools. It uses the normal
`max_steps` (default 20) and a ten-minute deadline. No separate six-step or
12-call limit applies. It does not summarize days, archive old records, prepare
embeddings or execute shell commands. A deterministic overview includes compact
conversation excerpts and tool names, with source/job IDs for optional details.
It includes up to 4000 bytes from the global archive and about 16000 across the
family archives (minimum 1600 per legacy scope), plus headings. There is no
extra summarization call. A successful run advances existing cursors only through
the represented batch; later entries, concurrent messages and failed batches
remain eligible. Raw tool output and full messages stay archived.

The final response is the dream report, already preserved in the archive.
`memory read part=dreams` lists attempts and results; read a job ID for its full
transcript. Ordinary family search includes global reflections. `harness status`
and `alina status` expose the last attempt, last completed reflection and next
scheduled time, distinguishing skipped work from actual reflection. No new events means no model call, except for active personal intentions
under enabled autonomy (at most once successfully per date). Personal exploration
still runs in separately budgeted one-shot initiatives.

Embeddings are optional. `POST /v1/memory/jobs` with `kind: "reindex"` indexes up to 100 current notes
per invocation, including legacy nuclei, through the configured endpoint. Only
notes and explicit search queries are sent there; ordinary prompt preparation
makes no embedding call. Exact cosine search is linear in indexed notes; the full
raw archive uses FTS5. Endpoint/model changes require reindexing; incompatible
vectors are never compared. Network failures fall back to text search.

Migration is additive. Old notes, nuclei, summaries, cited evidence and original
JSON files are retained. Old working transcripts and checkpoint files are imported
into shared search without rewriting them. Known matching events keep their dates;
otherwise imported events explicitly have no known timestamp. `today` and `week`
remain read aliases for date views, without lifecycle transitions. Old Markdown
views from 0.2 are retained snapshots and are no longer refreshed.

```sh
alina api POST /v1/memory/jobs '{"kind":"dream"}'
alina api GET '/v1/memory/read?q=dreams'
alina api GET '/v1/memory/read?q=focus'
alina api GET '/v1/memory/read?q=SOURCE_ID&offset=16000'
alina api GET '/v1/memory/search?q=backups'
alina api POST /v1/memory/focus '{"id":"NOTE_ID","pinned":true}'
alina api POST /v1/memory/jobs '{"kind":"reindex"}'
```

Storage remains private plaintext. Recognized credentials are redacted in the
memory archive; job and working-transcript files retain their existing behavior.
No automatic expiry or disk quota is imposed. Keep a state backup before upgrading;
do not open upgraded state with an older binary.

## Portable recurring tasks

The scheduler runs in the daemon; it does not require a system cron daemon.
Schedules persist in `tasks.json` and use five-field cron or descriptors such as
`@every 6h` (minimum interval one minute). A task is an agent request, subject to
the same shell permissions as interactive work. Each occurrence has a fresh
session and stable job ID; restarts do not replay a submitted occurrence.

```sh
alina api GET /v1/tasks
alina api POST /v1/tasks '{"name":"Disk check","cron":"0 9 * * *","prompt":"Check local free disk space","catch_up":true}'
alina api POST /v1/tasks/TASK_ID '{"action":"pause"}'
```

The agent manages user tasks through `schedule`. Use `alina api POST /v1/tasks` with `name`, `at` (RFC3339) and `prompt` for a single wake-up. A submitted one-shot is disabled;
its occurrence ID prevents replay after a crash. Personal wake-ups additionally
require an active intention ID and the enabled autonomy configuration. Catch-up
coalesces missed occurrences to one execution; it never replays every missed
tick. Executions of the same task do not overlap. Include `catch_up:true` in API requests to coalesce missed runs; the native
tool can enable or disable it. Scheduled Telegram tasks inherit the chat
owner and deliver results/approval requests there. System dream jobs are visible
through `alina api GET '/v1/status?scope=global'` when users are configured
(`alina status` in legacy mode); they do not send routine Telegram notifications.

Android suspension still suspends the daemon: the scheduler is portable, not an
Android exact-alarm service. The next wake-up coalesces a missed occurrence.

## Personal workspace and initiative

`workspace/notes`, `workspace/experiments` and `workspace/procedures` are Alina's
persistent working area. She can create scripts and short instructions with the
installed tools; `procedures/index.md` records where a capability lives, when it
was actually verified and its limits. No plugin runtime or new dependency is
required. Administrative credentials, grants and configuration remain separate.

The `memory` tool's `intentions` action lists personal projects; `intend` creates/updates one
with title, reason, next step, stopping condition and status `active/done/dropped`.
There are at most eight active intentions. They are exposed in the prompt and
through `alina api GET /v1/intentions` / Telegram `/intentions` and persist across restarts.
They do not represent user requests or confer permissions.

New quick setups enable personal exploration with 12 model calls per calendar
day, five minutes per run and web search. The scope is local tools and useful
procedures in Alina's personal workspace. Advanced setup can change or disable
it; existing installations retain their choice. Raw configuration defaults keep
exploration disabled until configured. Usage is reserved in SQLite before each inference,
including checkpoint calls, so retries and restarts cannot reset the daily budget.
An unsuccessful request still uses that reservation. This limits call count,
not tokens or monetary cost. Dream has its own separate budget described above.

Personal exploration uses one-shot wake-ups with `origin=self` and `intention_id`.
These jobs run quietly as owner `alina`, with ordinary results visible in status;
they do not send routine Telegram notifications. They require shell working
directories inside the personal workspace and reject declared network/download/
installation operations; configured web search is available separately. Shell
scope remains a cooperative contract under the same OS user, not filesystem
isolation. A wake-up checks that its intention is still active before submitting work.
If a budget expires, progress and the intention remain; resumption is explicit.

## Harness awareness and user-requested changes

The versioned [harness manual](../internal/alina/procedures/harness.md) ships in
the binary and is refreshed at startup in each workspace. Its index entry is
added without replacing personal procedures. The system prompt does not include
the manual. Alina can read it on demand, including during reflection through
`harness manual`. Live status/configuration and scoped diagnostics use that same
tool. Only user conversations may stage configuration changes and request a
self-restart. See [the lifecycle and recovery contract](operations.md#self-inspection-and-controlled-restart).
