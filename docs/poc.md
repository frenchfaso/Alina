# Running the POC

Build with Go 1.26 or newer. No Node.js, Python or Lua runtime is required by
Alina. External commands remain the user's installed programs.

```sh
go build -trimpath -o alina .
./alina setup
./alina serve
```

`setup` configures the default provider/model, working directory, all search
providers, Telegram, network policy and optional personal exploration budgets. It does not install system packages or enable services.
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

`alina resume JOB_ID` resumes interrupted/failed work in its original session,
with an explicit instruction to check current state before repeating effects.
`alina intentions` lists Alina's personal questions and projects.

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
`/revoke ID`, `/resume ID`, `/intentions`. Attachments are deliberately not fetched in this POC.

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
  inherited seccomp filter denying socket creation/connections (including Unix
  sockets), ptrace, cross-process memory writes and io_uring creation. Existing
  socket descriptors are not passed to the child. Filter installation failures
  stop execution. This also blocks network calls made inside scripts.
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
  variables. Shell output is capped at 48 KiB, command execution at 120 seconds
  by default. Process groups are killed on cancellation/timeout and after their
  foreground command exits. Deliberately daemonized descendants are outside the
  POC's process-group guarantees.
- Up to 16 jobs can be active. Each session processes messages in order;
  independent sessions can progress while another waits for approval or a shell
  process. One model request runs at a time, with foreground requests ahead of
  waiting dream/initiative calls. An in-flight model request is not preempted.
  Use a separate session (`ask -session NAME`, or `/new`) for an independent request
  while the current conversation is waiting for consent.
- Jobs are persisted in SQLite; status loads only active and 50 recent finished
  jobs. Old `jobs/*.json` are imported without overwriting existing IDs and retained
  as migration backups. Specific older jobs remain available by ID. Do not run
  a 0.1 binary against 0.2 state: it does not understand initiative budgets or
  the new job store. A rollback requires a matching backup of the state. Status
  `completed` means the turn finished; it is not independent proof of task success.
- Working context is compacted at model-call boundaries when its estimated size,
  including prompts and tool schemas, exceeds 75% of `context_tokens` (default
  32768). The estimate uses serialized bytes / 3, not a provider tokenizer;
  configure the budget for the selected model. Message count and elapsed days
  do not trigger compaction. Checkpoints preserve objectives, verified results,
  uncertainty and next actions. Whole tool exchanges stay together. The shared
  SQLite archive and earlier JSON transcripts remain intact on success or failure.
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

## Shared memory, attention and reflection — 0.3

Alina has one archive across CLI, Telegram, scheduled work and reflection. Source,
job, role and timestamp are provenance and reply-routing information, not recall
boundaries. Each working conversation still orders its tool exchanges and keeps
its immediate task context; a bounded excerpt from other conversations helps
continuity across channels. `/new` changes working context without erasing memory.
The POC remains a personal, single-owner service, not a multi-tenant memory store.

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
an explicit `supersedes` ID. Unknown sources are rejected. Superseded records and
notes directly citing them leave ordinary retrieval, but remain readable by ID.
A source citation establishes provenance, not that an interpretation is correct.

`soul.md` remains a short personal orientation (180 words / 1600 bytes), with a
last-valid fallback and revision history. A reflection uses the `soul` tool with
its exact previous text and a reason; a concurrent file edit prevents replacement.
It can finish naturally without changing the soul or creating any notes.

Dream defaults to `0 3 * * *`, with one catch-up after missed executions. It uses
**the same agent loop and imprinting as chat**, with memory, scheduling and soul
revision tools. It has six iterations, twelve total model calls including any
checkpoints, and ten minutes. It does not summarize days, archive old records,
prepare embeddings or execute shell commands. A successful run advances its
observed-event cursor; messages arriving during reflection remain eligible next
time. No new events means no model call, except for active personal intentions
under enabled autonomy (at most once successfully per date). Personal exploration
still runs in separately budgeted one-shot initiatives.

Embeddings are optional. `alina memory reindex` indexes up to 100 current notes
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
alina dream
alina memory read focus
alina memory read recent
alina memory read archive
alina memory read soul
alina memory read 2026-09-01
alina memory read SOURCE_ID 16000  # use the returned next_offset
alina memory search "What did we decide about backups?"
alina memory focus NOTE_ID
alina memory focus NOTE_ID pin
alina memory focus NOTE_ID unpin
alina memory reindex
```

Storage remains private plaintext. Recognized credentials are redacted in the
memory archive; job and working-transcript files retain their existing behavior.
No automatic expiry or disk quota is imposed. Keep a state backup before upgrading:
older binaries do not understand the new note schema and must not use 0.3 state.

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

The agent manages user tasks through `schedule`. Use `alina tasks once NAME AT
PROMPT` (AT in RFC3339) for a single wake-up. A submitted one-shot is disabled;
its occurrence ID prevents replay after a crash. Personal wake-ups additionally
require an active intention ID and the enabled autonomy configuration. Catch-up
coalesces missed occurrences to one execution; it never replays every missed
tick. Executions of the same task do not overlap. The terminal command defaults
to catch-up; the tool can disable it. Scheduled Telegram tasks inherit the chat
owner and deliver results/approval requests there. System dream jobs are visible
through `alina status`; they do not send routine Telegram notifications.

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
through `alina intentions` / Telegram `/intentions` and persist across restarts.
They do not represent user requests or confer permissions.

Setup can authorize a scope of personal exploration once. It defaults to disabled;
when enabled, defaults are 12 model calls per calendar day and five minutes per
run, with optional web search. Usage is reserved in SQLite before each inference,
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
