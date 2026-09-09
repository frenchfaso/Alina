# Operating and debugging Alina

Alina 0.10 has eight commands. The former commands are removed, without aliases.
Run `alina help` for the CLI and `alina api` for the local endpoint catalog.
There is no additional runtime, remote log collector or telemetry service.

| Command | Purpose |
| --- | --- |
| `setup` | Interactive onboarding; `setup login [browser]` reconnects ChatGPT |
| `serve` | Run the daemon in the foreground |
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
native user/family management. See [people and one global mind](people.md) for
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
Telegram `/resume` updates reuse the same job ID. Sending a message and saving
its receipt cannot be one transaction: a crash between them can still duplicate
a reply, and a partially sent long reply can repeat earlier chunks.

Delivery runs on pending approvals and completed jobs, drains the startup
backlog, then waits for an internal state-change signal instead of polling every
second. Failed deliveries retry after 5, 10, 20, 40 and then 60 seconds, capped
at 60 seconds until success; no new incoming message is needed to retry.
Incoming messages use a 50-second long poll (previously 25): Telegram returns
as soon as an update arrives. These waits do not invoke the language model.
