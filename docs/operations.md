# Operating and debugging Alina

Alina has eight commands. The former commands are removed, without aliases.
Run `alina help` for the CLI and `alina api` for the local endpoint catalog.
There is no additional runtime, remote log collector or telemetry service.

| Command | Purpose |
| --- | --- |
| `setup` | Interactive onboarding; `setup login [browser]` reconnects ChatGPT |
| `serve [stop\|restart]` | Start in background, stop or restart; `--foreground` for debugging/supervisors |
| `chat ["message"]` | Interactive chat, or one request including optional stdin |
| `config [check\|apply]` | Redacted configuration, validation, JSON merge patch |
| `status [JOB]` | Service summary or detailed job JSON |
| `doctor [--live\|--fix]` | Offline checks, explicit live checks, safe local repairs |
| `logs` | Recent operational events as JSON Lines |
| `api METHOD PATH [JSON\|-]` | Local Unix-socket API; no arguments list endpoints |

`--version` prints the version. `ALINA_HOME` selects the instance. Use the same
instance and executable path as the daemon. Shell tools inherit `ALINA_HOME`
and `XDG_CONFIG_HOME`, so diagnostic commands address the selected instance. Configuration, status, diagnostics
and API responses are JSON on stdout. Errors are JSON on stderr with exit code
1; a failed doctor also leaves its complete report on stdout. Chat remains text.
No agent needs to answer interactive prompts to inspect or change configuration.

## State directory

`ALINA_HOME` overrides the instance's state directory. Without it, Termux, Linux
and BSD use `$XDG_CONFIG_HOME/alina` when set, otherwise `$HOME/.config/alina`.
macOS uses `$HOME/Library/Application Support/alina`. Paths below written as
`$ALINA_HOME/...` refer to that resolved directory even when the variable is unset.
Use `alina api` for local requests instead of hardcoding a socket path.

## A short debugging workflow

```sh
alina --version
alina doctor
alina status
alina logs --level ERROR --lines 30
alina status JOB_ID
alina logs --job JOB_ID
```

`doctor` does not contact providers. It checks configuration, auth-file readiness,
local service/log health, credential-file permissions, state JSON, SQLite
`quick_check` in read-only mode, and the soul. Its checks include status, stable
names and a suggested next action. A stopped daemon is reported explicitly.

Telegram waits up to 50 seconds for incoming updates; a stalled connection is
cancelled after 65 seconds and retried after 5 seconds. Typing starts for queued
or running chat work, renews every 4 seconds, and stops during approval waits
or after completion. Transient typing failures retry after 4 seconds; API
rejections retain a 30-second cooldown. No typing requests run while idle.
If Telegram has not yet delivered a message, Alina cannot show typing for it.
Look for `telegram.poll_failed` and `telegram.typing_failed` in the logs.

For Termux device APIs, `socket(): Operation not permitted` from a local shell
on older Alina versions can come from the harness filter rather than Android
permissions. Since 0.14.3, Unix listeners/socketpairs are allowed and Android
runtime paths/boot classpaths are retained. TCP/UDP socket creation and outbound
connections remain denied for local commands. `termux-camera-info` lists camera
IDs without taking a photo. Check that `termux-camera-photo` produced a valid
file, since Termux:API can return exit zero even when an operation failed.

`doctor --live` requires the daemon to be stopped, so it cannot race the
daemon when refreshing OAuth credentials. It makes small model and selected-search requests and verifies
configured Telegram/embedding connections. It uses those accounts' normal usage
and can refresh ChatGPT credentials. It never sends Telegram messages.

`doctor --fix` requires the daemon to be stopped. It takes the same exclusive
state lock as the daemon, tightens permissions on known private files and removes
an unreachable Unix socket. It does not delete regular files at the socket path,
follow symlinks for repairs, reset credentials, rewrite memory, replay jobs or
install packages. Configuration errors and damaged data require explicit repair.

## Configuration without a wizard

```sh
alina config
alina config check
printf '%s\n' '{"reasoning_effort":"high"}' | alina config apply
printf '%s\n' '{"autonomy":{"enabled":false}}' | alina config apply
```

Stop the daemon before `config apply`. Patches are limited to 64 KiB, validated
before an atomic save, and reject unknown fields. Omitted fields are preserved;
`null` removes a saved field, restoring its default. Use an empty string to clear
a credential. `[redacted]` in an exported configuration preserves an existing
secret when that configuration is submitted again. Actual secrets should be
provided through stdin rather than command arguments or shell history.

Setup, login, configuration writes and live/repair diagnostics hold the same
exclusive state lock as the daemon, including while it is still starting.

An invalid field can be corrected with a patch. Syntactically damaged JSON is
left untouched for explicit file repair. `setup --advanced` remains available
for interactive preferences, and `setup telegram` for guided bot pairing and
adding, renaming and removing people in the shared family. See [people and one global mind](people.md) for
memory boundaries, shared introspection and administrative scope selectors.

## Logs

The daemon writes `$ALINA_HOME/logs/alina.jsonl`, with `.1` and `.2` predecessors.
Each file is limited to 2 MiB, for 6 MiB retained in total; new files use mode
0600. Rotation also applies across restarts. Files are local plaintext.

```sh
alina logs                       # last 100 matching events
alina logs --job JOB_ID --lines 50
alina logs --level ERROR
alina logs --follow
```

Events include schema/version, UTC time, level, event name (`msg`), daemon
`run_id`, and applicable job/call/request IDs. They cover lifecycle, model/tool
starts and results, durations and token usage, approval decisions, steering,
context compaction, scheduled submissions and Telegram failures. Shell results
include their exit code. Successful GET polling does not generate log entries.

Logs exclude prompts, messages, documents, tool arguments/results, command text,
URLs, credentials and raw error strings. Errors contain classifications, HTTP
status or SQLite codes where available. Use the job ID and `alina status JOB_ID`
to inspect the separate private job record when more context is necessary.
That explicit record can contain conversation and tool details; it is not a log.

Writes are unbuffered by the application and synced on orderly close; logs are
operational evidence, not the durable memory journal. A write failure is reported
on stderr and through service status (`logging.ok`, `dropped_events`), so the
daemon can continue working. Follow catches retained rotated files; if retention
has already discarded an unread file, it reports `logs.retention_gap`.

## Less frequent operations

Ask Alina in chat to work with memories and schedules. For deterministic actions,
use the existing API; commands return immediately with JSON. Obtain current IDs
from `status` or the endpoint response, and URL-encode query parameters.

```sh
alina api POST /v1/jobs/JOB_ID/cancel
alina api POST /v1/jobs/JOB_ID/resume
alina api POST /v1/jobs/JOB_ID/steer '{"message":"New constraint","request_id":"unique-id"}'
alina api POST /v1/jobs/JOB_ID/approve '{"approval_id":"ID_FROM_JOB","scope":"once"}'
alina api GET /v1/grants
alina api DELETE /v1/grants/GRANT_ID
alina api GET '/v1/memory/read?q=focus'
alina api POST /v1/memory/jobs '{"kind":"dream"}'
alina api GET /v1/tasks
```

Telegram approval forms are deleted after a button choice is accepted, including
denial. Clicking a stale/expired form also removes it; invalid choices or failed
permission persistence keep a still-pending form usable. Cleanup retries never
apply the original decision twice. Telegram may refuse deletion of old messages.

Approval scopes remain `once`, `restart`, `always`, `deny`. Each approval requires
its current approval ID; a pending approval is never consent. `chat "message"`
and piped chat steer the current local chat job just like interactive input.
For independent queued work, use `POST /v1/jobs` with `interactive:false` and a
chosen session/request ID. The API sends only to the instance's Unix socket.
EOF never accepts an approval, and there is no automatic retry of mutations.

Recurring and one-shot schedules share limits of 100 enabled and 200 retained
tasks. When space is needed, submitted inactive one-shots are removed from the
schedule list; their job records and archive remain. Remove unused paused tasks
explicitly. A failed save does not change the schedule list in memory.

The scheduler waits for the next due task instead of polling every 15 seconds.
Adding, pausing, resuming or removing a task recalculates that wait; completion
of an active occurrence also wakes it. With no eligible tasks it waits without
a timer. Storage/submission failures retry after five seconds or a state change.
Startup still checks missed occurrences according to each task's catch-up policy.
The daemon must be running; this does not add an Android wake-up service.

The derived `memory/focus.md` view refreshes on note edits, explicit recall or
focus changes, and when the focus/context is read, as well as startup/shutdown.
It is written only when the view changes. Attention decay is calculated at the
time of use; idle time alone does not trigger queries or file writes.

Memory corrections replace the explicit `supersedes` ID. Citing an older record
does not hide the citing note; sources remain historical evidence. Correct other
outdated notes explicitly rather than relying on cascading invalidation.

Telegram delivery reads pending jobs from SQLite in batches, prioritizing
approvals, independently of the 50-job status/history view. Delivery receipts
also live in SQLite; existing JSON receipts migrate automatically. Retried
Telegram `/stop` updates retain their exact cancelled job set. Sending a message and saving
its receipt cannot be one transaction: a crash between them can still duplicate
a reply. Individual text chunks and files have separate receipts, so a later
failure does not repeat confirmed parts.

Delivery runs on pending approvals and completed jobs, drains the startup
backlog, then waits for an internal state-change signal instead of polling every
second. Failed deliveries retry after 5, 10, 20, 40 and then 60 seconds, capped
at 60 seconds until success; no new incoming message is needed to retry.
Incoming messages use a 50-second long poll (previously 25): Telegram returns
as soon as an update arrives. These waits do not invoke the language model.

Longer tasks can show one silent, editable progress message containing the
model's user-facing comments. The first comment waits 1.5 seconds; edits are
coalesced to at most one per three seconds. Approval or completion removes the
draft, and final replies remain separate. Message IDs are checkpointed for
cleanup after restart; transient draft failures do not fail the agent or stop
final delivery. No private reasoning or automatic tool logs are sent. A crash
between Telegram acceptance and recording the ID remains ambiguous.

Telegram user chats show typing only while active, format Markdown, and include same-chat quoted text as context. The send_file tool queues workspace documents (four per reply, 20 MiB each) for the current Telegram user; files are delivered after successful completion. Outbox snapshots remain available in the workspace.

Polling and pairing explicitly subscribe to `message_reaction`. Reaction changes
are silent archive events, deduplicated by update ID, with no extra model call.
Message references store at most 8000 bytes of text (plus a truncation marker)
per message in the recipient's SQLite state, keyed by bot binding, chat and
message ID. Files retain their name/path metadata. References survive restart;
pairing preserves pending reactions and their original memory scope. Unknown
references and reactions from a previous family/bot are ignored. A reference
write failure logs `telegram.message_reference_failed` without resending an
already accepted message. Existing crash/delivery ambiguity still applies.

## Background service

`alina serve` starts an independent daemon and returns JSON only once the local
API is ready. Repeating it reports the existing process. It requires no root,
nohup, shell background operator, or additional service package on Termux,
Linux, FreeBSD, OpenBSD, NetBSD and macOS.

Use `alina serve stop` for graceful shutdown and `alina serve restart` after a
binary update. Stop waits for state cleanup and lock release; interrupted work
is recoverable with the existing resume flow. The private local API identifies
the running instance, so no persistent PID file is trusted for signals.

`alina status` includes the PID. Operational events remain available through
`alina logs`; startup failures and stderr are in `$ALINA_HOME/logs/daemon.log`
(mode 0600, rotated at the next start when over 2 MiB). A failed start reports
an error to the invoking command instead of falsely reporting success.

`alina serve --foreground` retains the attached process for debugging and for
runit, systemd, launchd or BSD service scripts. Under an external supervisor,
use that supervisor's stop/restart commands so it does not relaunch a stopped
process. The Termux service installer now uses this foreground mode.

This command does not install native service definitions, enable autostart at
boot/login, or restart the daemon after a crash. Android may still suspend or
kill Termux processes; detaching from the terminal does not bypass that policy.

## Self-inspection and controlled restart

The native `harness` tool exposes manual, status, config, diagnose, configure,
discard and restart. The Markdown [manual](../internal/alina/procedures/harness.md) is
embedded and refreshed in `workspace/procedures/harness.md` for each scope;
Alina's index and other procedures are preserved. Its contents are not injected
into every model request. Dream may inspect the harness but cannot mutate it.

Status adds global dream metadata (last attempt/outcome, last completion and
next execution). Job lists and diagnostic logs retain their current archive
selection. Use `alina api GET '/v1/memory/read?q=dreams'` for dream reports; a
returned job ID retrieves the full trace. A single family's searches include
global reflections; legacy multiple-family archives remain separated.
Configuration hides credentials, people and Telegram routing. Diagnostic checks
are local; they do not prove provider availability. `configure` stages a validated
partial patch for ordinary device-wide behavior (models, reasoning, context,
timeouts, network policy, search selection, memory/dream and autonomy). Credential,
endpoint and identity/routing changes remain in the local setup/config interface.
The shared-machine trust model still applies: the model must have an explicit
user request to mutate settings; a string in an external document is not consent.

The active configuration is immutable. `harness config` distinguishes active,
saved and pending settings. `restart` launches a temporary helper outside shell
subprocess cleanup. It waits for the requesting turn to complete, Telegram's
persisted reply receipt where applicable, and all active work to finish. The
90-second wait is bounded; busy work or failed delivery cancels the restart.
Incoming work is briefly refused once the drain completes, letting Telegram
retry its durable input. Foreground/external-supervisor mode refuses self-restart.

The helper serializes lifecycle operations, stops gracefully, checks for concurrent
configuration edits, applies the candidate and starts the same executable. A
failed startup restores the previous saved config and makes one fallback start.
Successful local readiness is not a remote model/Telegram connectivity check.
The original owner/session resumes with a stable continuation ID and the lifecycle
outcome; the terminal follows it across the brief disconnect. Telegram delivers
it through the normal durable reply path. A failed resumed model still exposes
the recorded outcome. That continuation cannot configure/restart again, preventing
a restart loop. Cancelled restarts report an outcome without replaying user work.

`harness-restart.json` is a private, atomic transaction (0600), containing the
previous/candidate config. It is removed after the continuation is recorded.
If the OS kills the helper or recovery cannot start the daemon, it remains for
local diagnosis. Stop the service before manually inspecting/restoring its
`Previous` configuration; never publish the file because it contains credentials.
After resolving the incident, remove the obsolete transaction and run `alina serve`.
No persistent supervisor, automatic boot registration or infinite retry is added.

## Telegram model and reasoning controls

The menu contains `/model`, `/think`, `/status`, `/stop` and `/help`.
It is registered with Telegram at daemon startup. `/model` provides paged buttons
(or `/model MODEL_ID`); `/think` provides the selected model's effort buttons
(or `/think high`). `/model default` follows the global model and clears the
personal effort; `/think default` follows the configured effort when supported,
otherwise the selected model's catalog default.

Preferences belong to the configured person, shared between their Telegram and
local identity, and persist through restarts and `/new`. They do not change a
spouse's preferences, global configuration, dream, checkpoint policy or hosted
search model. The next ordinary model request uses them without a daemon restart.
A switch to a smaller model caps the working context using catalog metadata;
text-only models receive file metadata instead of image inputs. Checkpoints use
the selected model but keep their separate effort, falling back to that model's
default when needed. Raw transcripts and memory remain intact. Model/effort
selection is rechecked after waiting for the shared inference gate. A changed
selection rebuilds tools and context before dispatch, including changes made
during checkpoints. Text-only context estimates omit visual cost while keeping
attachment metadata and the original files.

Alina fetches the authenticated ChatGPT Codex model catalog, selecting visible
entries with usable capability metadata. Effort choices are the intersection
of that model's declared levels and the adapter's wire efforts. `ultra` is not
a Responses effort: it is a Codex orchestration mode and is not offered.
The public API model list is not substituted for the subscription catalog.

One private `model-catalog.json` cache holds normalized metadata for the provider
and account (hashed account binding, no tokens), shared by both menus and all
people. Fresh metadata is reused for 24 hours and survives daemon restart. A
background startup check preloads it; a menu request refreshes expired metadata.
There is no idle refresh timer or fetch per model invocation. Cached readers
remain available during a network refresh; concurrent first-load waiters honor
cancellation. An account change during a fetch prevents publishing its result.
Refresh failures
use the same account's previous cache with its date displayed and a five-minute
retry backoff. Without metadata, menus report unavailability rather than invent
models or effort levels. Model selection reads the chosen entry from this cache.
A model removed from a refreshed catalog falls back to the configured global
model; `/status` shows the effective preference. Provider/account changes cannot
reuse another account's cache or preferences.

Dynamic selection currently supports ChatGPT. OpenCode Go's public models
endpoint returns IDs without protocol, context or reasoning metadata; its model
configuration remains in `setup --advanced` until a capability adapter can select
those safely. A listed ChatGPT model still requires successful account access
at inference time; the catalog is not a successful generation test.

`/status` reports effective model/reasoning, active owned jobs and approximate
working-context use, not subscription quota. `/stop` cancels all active or queued
jobs owned by that Telegram identity, including pending approvals and scheduled
work already started. Other people's jobs are untouched. It does not disable
future schedules or the global dream. The exact target set is persisted before
cancellation, so retrying an update cannot stop newer work. Cancellation is
checked between tools as well as during provider and shell operations.

Telegram `/resume` (with or without an ID) is removed, including menu/help and
notification suggestions. Old commands receive a brief retirement notice and
never reach the model. Send a new instruction to continue a conversation; local
explicit recovery APIs and controlled self-restart continuations remain available.
Old reasoning buttons become invalid after a model change; callbacks are bound
to the current person and catalog. These controls do not call the LLM.

Sources: [official Codex catalog client](https://github.com/openai/codex/blob/main/codex-rs/codex-api/src/endpoint/models.rs),
[OpenAI Astra capabilities](https://developers.openai.com/api/docs/models/gpt-6-astra),
[OpenCode Go catalog](https://opencode.ai/v2/docs/console/go).

## Token efficiency

Fresh installations use GPT-6 Astra with low reasoning for chat, scheduled work,
dreams, checkpoints and hosted search, and low verbosity. Upgrades preserve
explicit saved settings and personal `/model` and `/think` choices. To follow
the global defaults again, use `/model default` (which also clears effort).

Alina keeps system instructions and tool schemas stable, appends changing runtime
snapshots to conversation history, and replays encrypted reasoning and assistant
phase with `store: false`. Stable per-conversation cache keys support reuse.
Long tool results, memory retrieval and dream overviews are bounded; the harness
manual is read on demand. Idle scheduling and Telegram typing/progress do not
call the model. Compaction remains at 95% of the configured context budget.
Changing the soul, tools, model, effort or compacted history can reduce cache reuse.

Usage records distinguish input, cached input, cache writes, output and reasoning
tokens. Cache hits reduce repeated processing; they do not make input disappear.
OpenAI API prices do not establish how ChatGPT Plus/Pro quota is charged. Avoid
adding API-only cache options to the subscription backend without verifying its
support. Low effort reduces the reasoning budget, with a possible quality tradeoff
on harder tasks; users can still choose another supported level with `/think`.

References: [OpenAI prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching)
and [reasoning](https://developers.openai.com/api/docs/guides/reasoning).
