# POC verification

Entries below are dated release snapshots, not a live device inventory. Earlier
installation paths, retained binaries and account states describe those test
runs; they may since have changed.

## Astra low defaults — 0.15.2 (2026-09-11)

- Default chat, scheduled work and dream reasoning now use low, matching
  checkpoints and hosted search. The provider fallback is also low. Model remains
  GPT-6 Astra; context remains 272000 with compaction above 95%.
- Full macOS `go test -race ./...` and `go vet ./...` passed. Request fixtures
  verify low effort for chat/checkpoints/search, reasoning/phase replay and usage
  accounting; dream and personal-default reset tests also passed.
- Android arm64 binary cross-built successfully. No native run or live provider
  benchmark was performed for this release: the Galaxy A15 SSH endpoint timed
  out on three connection attempts. Deployment and resetting its saved global
  and personal model/effort settings are still pending; last verified device
  version remains 0.15.1.
- Token-efficiency audit checked the existing stable-prefix, stateless reasoning
  replay, bounded context and lazy documentation design against official OpenAI
  guidance. No backend-unverified API cache parameters were added. See
  [token efficiency](operations.md#token-efficiency) for limits.

## Validation status at 0.15.1

- The full macOS race suite passed for 0.15.0; final dream/harness/progress
  regressions and Go vet also passed after the last metadata changes. Earlier
  native Galaxy tests and platform build checks are recorded per release below.
  BSD has cross-build coverage, not native runtime validation.
- Real ChatGPT login, Astra inference and OpenAI hosted search passed during
  the [0.11 setup check](#telegram-essentials-and-setup-recovery--0110).
  Telegram conversation use has been reported by the user; the 0.14.2 checks
  additionally verified bot access and authenticated model-catalog loading.
  Automated delivery tests use fixtures. Actual typing visibility and model
  button rendering are not established by those tests.
- The latest recorded Galaxy deployment is 0.15.1, with only the current
  executable retained. Camera metadata and a temporary JPEG were verified
  in 0.14.3 through the corrected shell environment/filter. The image was not sent.
- Live OpenCode Go, Tavily, Brave and remote embeddings remain unverified in
  this record. Tests do not establish long-term memory quality, reflection
  quality, battery impact or peak resource use under sustained load.

For another installation, `alina setup` checks its configured accounts.
`alina doctor --live` repeats integration checks with the daemon stopped and
uses normal provider usage; it never sends Telegram messages. A private user
message to the bot checks the complete reply path. See [operations](operations.md).

## Dismiss completed approval forms — 0.15.1 (2026-09-10)

Telegram deletes the approval message after a successful button decision,
including denial. Stale/expired clicks also dismiss the form. Invalid choices
and failed consent persistence leave a pending form usable. A transient deletion
failure retries the callback without reapplying the decision; an expired callback
acknowledgement does not prevent cleanup. Telegram deletion restrictions still apply.

The Telegram/consent race regressions and Go vet passed. Two tests passed natively
on Galaxy A15, including nine approval-form cases: once, restart, always, deny,
expired, stale, invalid choice, unauthorized user and retry. The specific completed
form reported by the user was deleted successfully through the real Telegram API;
no new test message was sent. 0.15.1 was installed with no active jobs, doctor
passed, configuration was unchanged, and temporary test/rollback files were removed.
Installed Android SHA-256:
`7502487c2cbc0a6e734ecf49a0cbd023893fbc918caf3f583ec5e2acb4765375`.

## Shared dreams and Telegram progress — 0.15.0 (2026-09-10)

The Galaxy's 03:00 dream on 0.14.4 failed after 20.964 seconds: all six steps
were spent reading intentions and raw archive pages. Provider input across those
requests totaled 69,491 tokens (44,672 cached); no reflection was completed.

0.15.0 replaces those raw pages with a deterministic bounded overview and uses
the normal turn step budget. No auxiliary summarizer is added. Successful turns
advance only the represented batch; failed batches and concurrent experiences
remain eligible. Final reflections and full traces are shared through existing
memory tools in single-family installations. Status exposes execution evidence;
new setup users automatically join the same family. Existing storage stays in
place, with legacy multi-family boundaries retained for compatibility.

Nine selected tests passed natively on Galaxy A15, including bounded views with
large raw tool output, normal dream budgets, shared history/search, failure and
concurrent-event cursors, legacy personal archive migration, no-change reflection,
pairing preservation, Telegram draft editing/final delivery, restart cleanup
failure, and loop commentary. Telegram and provider calls in these tests use
fixtures; they do not establish actual draft visibility in a user's chat.

The full macOS race suite passed, followed by targeted race regressions and Go
vet after the last status changes. Android/arm64 build passed. All 42 local
Markdown links checked resolve. No real Telegram test messages were sent.

The daemon was updated to 0.15.0 with no active jobs; configuration hash was
unchanged and offline doctor passed. A real Astra/high dream then completed in
10.018 seconds, using 15,365 input tokens (7,552 cached), 185 output tokens and
69 reported reasoning tokens. It produced a reflection without changing soul.
This is a bounded batch, not a like-for-like benchmark against the prior failed
attempt: remaining experiences stay eligible for later dreams.

The completion and successful batch cursor were recorded. From the default
family API, the report was listed and found by search; the complete transcript
was read over two pages. Status reported the successful attempt, completion time
and the next 03:00 Europe/Rome run. No jobs remained active. Temporary test and
rollback files were removed, leaving only the current executable. Actual Telegram
progress-message visibility still needs verification in a real conversation.
Installed Android SHA-256:
`6919e0a131725c7a09ab3aecdcd8890ed3b8c9e24ced6db152f8b7a93561a68f`.

## Telegram reaction feedback — 0.14.4 (2026-09-10)

The full macOS race suite and Go vet pass; the Android/arm64 build also passes.
Five new regression tests cover
received reaction additions/removals and duplicate delivery, attributed family
recall and dream visibility, identity/bot/scope boundaries, preserving reactions
during pairing, unchanged pending approvals, and outgoing document references.
All Telegram requests use fixtures; no real messages or reactions were sent.

Reactions are recorded without starting or steering jobs or sending a reply.
They reuse the archive and existing context builder, with no new dependency or
model loop. The Bot API must deliver `message_reaction` updates for this to work;
actual event delivery in the users' private chats still needs live verification.
Old messages without a stored reference cannot be reconstructed through this API.
Eight selected tests also passed natively on Galaxy A15 Termux: the five new
reaction regressions plus pairing preservation, formatting fallback and file
delivery/retry. With no active jobs across configured scopes, 0.14.4 was installed
and started successfully. Offline doctor passed and the saved configuration hash
was unchanged. A read-only Telegram check confirmed API access, no webhook and
an active `message_reaction` subscription. No real Telegram message was sent.
Temporary test and rollback files were removed; only the current binary remains.
Installed Android SHA-256:
`8a69efe5da09f56274f11ea2528d37b9b7493eb0b9f9414afc2a930bf92db2e8`.

## Termux device API access — 0.14.3 (2026-09-09)

The user's camera attempt on Galaxy A15 failed with `socket(): Operation not
permitted`, despite valid Android permission and working direct camera metadata
queries. Reproduction found two harness causes: seccomp denied local Unix
listeners used by Termux:API, and the shell environment omitted Android runtime
paths/boot classpaths required by `am`/`app_process`. Enabling only local listeners
still timed out until the platform environment was restored.

The corrected filter permits Unix sockets/socketpairs while keeping outbound
`connect`, Internet/other socket families, ptrace, cross-process writes and
io_uring creation denied. A native subprocess regression checks Unix socketpair
transfer and incoming local results, plus rejection of IPv4, IPv6, netlink,
packet sockets and outbound Unix connect. The shell environment regression
retains Android runtime values while still excluding arbitrary API credentials.

On the Galaxy, the corrected filter plus the sanitized platform environment
returned camera metadata and captured a front-camera JPEG (436,059 bytes,
JPEG signature checked). The temporary photo was deleted without being copied,
displayed or sent. Android property-access warnings accompanied the successful
result. The existing shell network/timeout test and new Unix IPC test passed
natively. Focused macOS race tests and vet passed; Linux/amd64 vet/build and
Android/arm64 build passed. Android-specific opt-in metadata testing is included
and does not take a photo.

The final build passed all four selected tests natively on Galaxy A15, including
the opt-in camera metadata call through `runShell` (four camera IDs returned).
With no active work across configured people/global scopes, the device was
upgraded to 0.14.3 and restarted. Offline doctor passed, the saved configuration
hash stayed unchanged, and only the current executable remains in the deployment
directory. No Telegram message was sent by these tests.

## Telegram network recovery — 0.14.2 (2026-09-09)

Live Galaxy A15 logs on 0.12.0 showed Telegram polling network errors followed
by a roughly three-minute stalled request. After reception, the affected turn
completed in 11 seconds. No typing failure was logged for that turn; the logs
cannot establish whether the client displayed the indicator.

The receiver now has a 65-second deadline around its unchanged 50-second long
poll. Typing also covers queued chats, allows five seconds for the HTTP request,
and retries transient failures after four seconds; API rejections retain the
30-second cooldown. Idle operation still sends no typing requests.

The full macOS ARM64 race suite passed: 178 top-level tests passed, one optional
public-web smoke skipped, zero failures. Go vet, gofmt and diff checks passed;
the CGO-free Android/arm64 binary compiled. Five new regressions cover typing
retries/renewal and rejection cooldown, slow connections, queued family routing
and cancellation, shutdown during an in-flight request, and reconnecting after
a stalled long poll. These use virtual time and fake transports.

All six selected Telegram typing/polling tests also passed natively on Galaxy
A15 Termux. After verifying no active jobs across people/global scopes, the
device was upgraded from 0.12.0 to 0.14.2 and restarted. The saved configuration
hash was unchanged. Offline doctor passed; live read-only Telegram checks
confirmed bot access, no webhook/pending updates, and the five registered
commands. Authenticated model-catalog loading succeeded. Only the current
`alina` executable remains in the deployment directory; temporary tests and
rollback binary were removed. No Telegram test message was sent. Actual typing
visibility still requires a user interaction in the Telegram client.

## Review fixes and Telegram stop — 0.14.1 (2026-09-09)

The full macOS ARM64 race suite passed: 173 top-level tests passed, one optional
public-web smoke skipped, zero failures (174 total). Go vet, gofmt and diff
checks passed. CGO-free builds passed for Android/arm64, Linux/arm64,
FreeBSD/amd64, OpenBSD/amd64 and NetBSD/amd64.

Eight new regressions cover text-only attachment context, cached reads during a
stalled refresh, first-load cancellation, account changes during refresh,
model/effort changes while queued and during checkpoints, stopping multiple
owned active/queued jobs without affecting another person, and cancelling a
pending approval. Existing stop retry tests still pass. The former Telegram
resume retry test now verifies that the removed command never starts work.

The fixes address the [0.14 review](review-0.14.md). All transports use fixtures
and state is disposable. No live account-backed inference or Telegram messages
were sent. The Galaxy A15 was not updated or restarted; cross-builds do not
replace native device validation.

## Personal Telegram model controls — 0.14 (2026-09-09)

The full macOS ARM64 race suite passed: 165 top-level tests passed and the
optional public-web smoke was skipped (166 total). After final prompt-stability
and unknown-catalog-model fixes, the model-control and harness regressions passed
again with the race detector. Go vet, formatting and diff checks passed.

New regressions cover concurrent catalog caching, persistence and account
isolation, stale-cache retry backoff, personal model/effort on the provider wire,
context and vision adaptation, unavailable capabilities, stale Telegram buttons,
and a failed stop acknowledgement retried while newer work is active.
Tests use disposable state and fake authenticated provider/Telegram transports;
no real Telegram messages or account-backed inference requests were sent.

CGO-free builds passed for Android/arm64, Linux/arm64, FreeBSD/amd64,
OpenBSD/amd64 and NetBSD/amd64. These are compilation checks, not native tests.
The Galaxy A15 was not updated or restarted. Real account catalog discovery and
Telegram button rendering remain to be checked on the installed instance.
See [control behavior and limits](operations.md#telegram-model-and-reasoning-controls).

## Harness awareness and controlled self-restart — 0.13 (2026-09-09)

Validation uses disposable local state, fake providers and native subprocesses.
The macOS ARM64 race suite and Go vet pass. Eight new top-level regression tests
cover configuration/privacy boundaries, manual refresh without losing personal
procedures, delivery/idle draining, real detached restart and startup rollback,
concurrent saved-config edits, idempotent continuation/cancellation, Telegram
input retry, and reconnection in both terminal modes. A failed resumed model
retains the lifecycle outcome for the user. There are 162 top-level tests in the
suite; optional external-network smokes remain opt-in.

CGO-free builds pass for Android/arm64, Linux/arm64 and FreeBSD/OpenBSD/NetBSD
amd64. These are compilation checks, not native execution on those platforms.
This update does not deploy to or restart the configured Galaxy A15 instance.
No real Telegram messages or account-backed model requests are sent by these
tests. See [implementation and limits](review-0.13.md).

## Detached daemon lifecycle — 0.12.0

- Full macOS race suite passed (including real detached subprocess lifecycle);
  Go vet, gofmt and git diff checks passed.
- Cross-builds passed: Android/arm64, Linux/arm64, FreeBSD/amd64,
  OpenBSD/amd64 and NetBSD/amd64. macOS was tested natively.
- Galaxy A15: all 154 top-level tests passed with ALINA_TEST_WEB=1. Coverage
  includes foreground compatibility, detached startup/readiness, surviving the
  launching command, idempotent start/stop, restart, and failed-start cleanup.
- Installed 0.12.0 at ~/alina-poc/bin/alina; previous binary saved as
  alina-v011-before-daemon. No live service was running at installation, and
  none was started against real user data. No autostart or supervisor was added.
- The Termux service installer passes shell syntax validation and uses
  serve --foreground for compatibility with external runit supervision.

## Telegram essentials and setup recovery — 0.11.0

- Full macOS race suite passed; the subsequently added family/tool-visibility
  regression also passed with the race detector. Go vet and formatting checks passed.
- Galaxy A15: 151 top-level tests passed, zero failures/skips, with
  ALINA_TEST_WEB=1. The additional family/tool-visibility test passed separately
  on Android (152 tested cases in total).
- Real Galaxy `alina setup --no-start` completed successfully: ChatGPT connected,
  Telegram connected, Astra response and OpenAI web search verified, then Pronta.
  Existing two users and family configuration were preserved. Telegram had been
  disabled in saved configuration; getMe/getWebhookInfo verified it before
  restoring enabled=true under the daemon lock, with a private backup.
- Telegram delivery tests use fake transports; no real chat messages or files
  were sent. The service was not started by these checks.
- 0.11.0 installed at ~/alina-poc/bin/alina; prior 0.10.3 saved alongside it.

## General review and recovery fixes — 0.10.1

All **143 tests** pass natively on Galaxy A15 Termux, including the public HTTPS
smoke (zero failures or skips). The full macOS ARM64 race suite, Go vet,
formatting and diff checks pass. CGO-free builds pass for Android/arm64,
Linux/arm64, macOS/arm64 and FreeBSD/OpenBSD/NetBSD amd64.

Five new regression tests reproduced the baseline failures before the fixes:
local identity changing with the default user, divergent/lost soul recovery,
discarded messages immediately after pairing, a permanently rejected command
reply blocking other Telegram input, and diagnostic repair following a directory
symlink. See the [review and corrections](review-0.10.1.md).

Disposable legacy and multi-user binary smokes pass on macOS and the Galaxy.
They exercise CLI/configuration, daemon lifecycle, private logs, local API,
memory, scoped tasks, shared family recall, one soul/dream, membership changes,
restart and diagnostics. Galaxy RSS was **25,940 KiB** for the four-person
workload, and 22,140 KiB for the legacy smoke; these are small-workload samples.

No live model account or actual Telegram delivery was used. The default Galaxy
state directory had no configuration or ChatGPT login. Live integration and
behavioral tests require the first setup or a supplied configured instance path.
Version 0.10.1 is installed at `~/alina-poc/bin/alina`; the prior binary is
`~/alina-poc/bin/alina-v010-dc4f7ae4`. No daemon was running or started, and account
configuration and system services were unchanged. Android SHA-256:
`cb40bfe4428de9d96cfa05adc6c3051666d8c2e7f5fa246a574354793ab7ba4d`.

## Native people/families and one global mind — 0.10

All **138 tests** pass natively on Galaxy A15 Termux, including the opt-in public
HTTPS smoke (zero failures or skips). The full macOS ARM64 race suite, Go vet,
formatting and diff checks pass. CGO-free builds pass for Android/arm64,
Linux/arm64, macOS/arm64 and FreeBSD/OpenBSD/NetBSD amd64.

Twelve new regressions cover shared family recall, separate personal/family
archives, speaker attribution, native file/memory scope restrictions, one global
soul/dream and reflection cursors, scoped local administration, membership
changes and retained history, requester-owned Telegram approvals, a global
initiative budget, setup inbox persistence/replay and configuration validation.
They also cover inactive legacy receipts and a blocked bot recipient with a
50-job backlog without starving another person's reply.

Both legacy and multi-user disposable binary smoke tests pass on macOS and the
Galaxy. The multi-user smoke exercises four people in three memory scopes,
global empty dream, one physical soul file, scoped jobs/tasks/search, restart,
family reassignment and diagnostics of inactive archives. Galaxy RSS was
**25,484 KiB** for this small workload (legacy smoke: 21,376 KiB); this is not a
peak bound or real-model performance measurement.

Provider/OAuth/Telegram requests use fixtures; no live accounts or real Telegram
messages were used. These tests establish routing and storage behavior, not the
model's discretion or the quality of cross-family reflection. Shared shell/API
access and soul discretion are the explicit soft boundaries in this design.
See [people and one global mind](people.md).

Version 0.10.0 is installed at `~/alina-poc/bin/alina`, with 0.9.2 retained as
`~/alina-poc/bin/alina-v092-c59ccd54`. No daemon was running, and no accounts,
packages or system services were changed. Pair actual people with
`alina setup telegram` before starting the configured service. Android SHA-256:
`dc4f7ae4de36549f297a9131a76897db0992cc71e67728cba04139069ae67268`.

## Idle scheduling and Telegram delivery — 0.9.2

All **126 tests** pass natively on Galaxy A15 Termux, including the opt-in public
HTTPS smoke (zero failures or skips). The full macOS ARM64 race suite, Go vet,
formatting and diff checks pass. CGO-free builds pass for Android/arm64,
Linux/arm64, macOS/arm64 and FreeBSD/OpenBSD/NetBSD amd64.

Seven new regressions cover task deadline changes, waiting for an active
occurrence, a failed scheduler checkpoint without replay, delivery wake-ups and
exponential retries, immediate messages during long polling, and focus-view
updates on use. A simulated-clock test advances 24 idle hours after closing the
database: scheduler and delivery perform no storage polling. Tests also retain
startup backlog recovery and receipt deduplication from the earlier suite.

Disposable daemon smoke tests pass on macOS and the Galaxy. The phone's small
smoke workload used **21,544 KiB RSS**, not a peak bound or battery measurement.
Provider/OAuth/Telegram requests use fixtures; no live accounts or Telegram
messages were used. The 50-second long poll was verified through a fixture.

Version 0.9.2 is installed at `~/alina-poc/bin/alina`, with 0.9.1 retained as
`~/alina-poc/bin/alina-v091-40d169b5`. No Alina daemon was running, and no packages,
system services or account configuration were changed. Android SHA-256:
`c59ccd5471df7daa201b66fa521bdff0a0dde361ab95856908c2a5f7dd1135a9`.

## Reliability and simplicity review — 0.9.1

All **119 tests** pass natively on Galaxy A15 Termux, including the opt-in
public HTTPS smoke (zero failures or skips). The full macOS ARM64 race suite,
Go vet, formatting and diff checks pass. CGO-free builds pass for Android/arm64,
Linux/arm64, macOS/arm64 and FreeBSD/OpenBSD/NetBSD amd64.

Sixteen added regression tests cover exclusive setup/login locking, scheduler
rollback and limits, repaired-file integrity checks, compact Telegram consent
callbacks, idempotent resume, expired callbacks, a 61-job notification backlog
with migrated receipts, explicit memory corrections citing old evidence, shared
archive pagination, HTTP connection reuse, terminal SSE events, provider-protocol
changes, UTF-8 web documents and rejection of trailing API JSON.

Binary smoke tests on macOS and Termux pass configuration/CLI, daemon lifecycle,
private correlated logs, safe repair, scheduling, recall and restart/resume.
The phone's small smoke workload used **21,072 KiB RSS**; this is not a peak
bound or a real-model performance measurement. Provider/OAuth/Telegram calls
use fixtures; no live accounts or Telegram messages were used.

Version 0.9.1 is installed at `~/alina-poc/bin/alina`; the prior 0.9 binary is
retained as `~/alina-poc/bin/alina-v09-2141d750`. No Alina daemon was running,
and no packages, system services or account configuration were changed.
Android SHA-256:
`40d169b5d9a57742aa2270cba3162282d86accb352cffa6134ab7f2f3b18e61f`.
See the [review and architectural assessment](review-2026-09-09.md).

## Minimal CLI and operational logs — 0.9

The full suite passes with the macOS ARM64 race detector and Go vet. Formatting
and diff checks pass, with CGO-free builds for Android/arm64, Linux/arm64 and
FreeBSD/OpenBSD/NetBSD amd64. Native Termux verification uses the same suite,
including the opt-in public HTTPS test. Provider/OAuth/Telegram checks use
fixtures, with no live account calls or Telegram messages.

New regressions cover removed command aliases, stdin chat and JSON API errors,
redacted configuration round-trips, atomic validation/repair, daemon-lock
exclusion, live-check OAuth exclusion, Telegram webhook conflicts, private
correlated events, HTTP/SQLite error classifications (including SQLite errors
that implement the network timeout interface), concurrent log rotation, log
write failures, filters, follow across rotation and partial lines, safe doctor
repairs, read-only SQLite checks and instance propagation to shell tools.

The disposable binary smoke on macOS and Galaxy A15 covers config apply/check,
status and API discovery, lock rejection, idle reflection, scheduled work,
failed-job diagnostics, secret-free JSONL logs, permission repair, memory recall,
and persistence/resume. PTY checks confirm hidden tokens and terminal restoration
on Ctrl-C/SIGTERM. The phone's small smoke workload used 21,704 KiB RSS; this is
not a peak-memory bound.

Version 0.9 is installed at `~/alina-poc/bin/alina`, with 0.8 retained as
`~/alina-poc/bin/alina-v08-d9105b69`. Android SHA-256:
`2141d7507addaa52b31d21038972a0ac7c0b44943ed00422e1b9718bdb64b643`.
No packages, system services or permanent account configuration were changed.
See [CLI and logs](operations.md) for the intentionally breaking command changes.

## Quick onboarding — 0.8

The full suite passes with the macOS ARM64 race detector and natively on the
Galaxy A15. The phone passes **93 tests**, including the opt-in public HTTPS
smoke. Go vet, formatting, diff checks and CGO-free builds for Android/arm64,
Linux/arm64 and FreeBSD/OpenBSD/NetBSD amd64 pass.

New setup tests exercise the complete device-login and Telegram-pairing flow,
existing account reuse, default capabilities, private files, configuration
preservation, transient model/search failures, explicit fallback or deferral,
EOF/cancellation, token retries, webhook conflicts and pairing isolation.
Provider and Telegram requests use fixtures; no real accounts or messages were
used. The quick setup itself performs real checks when the user runs it.

PTY smoke tests on macOS and Termux verify hidden tokens, Ctrl-C/SIGTERM,
restoration of terminal mode and no partial configuration save on cancellation.
The disposable daemon smoke also passes initialization, advanced setup,
private configuration, workspace persistence, idle reflection, one-shot tasks,
soul fallback, FTS recall, SQLite integrity and resume. The phone's small smoke
workload used 21,796 KiB RSS; this is not a peak-memory bound.

Version 0.8 is installed at `~/alina-poc/bin/alina`, retaining 0.7 as
`~/alina-poc/bin/alina-v07-c4f87ce3`. Android SHA-256:
`d9105b698d39bc2ad1e29c58347d655b53b9f70fb58d81370d253fa1d7293bd7`.
No packages, system services or permanent account configuration were changed.

## Web fetch and steering — 0.7

All **84 offline tests** pass with the macOS ARM64 race detector and natively
on Galaxy A15 Termux. The additional opt-in public HTTPS smoke also passes on
the phone (`ALINA_TEST_WEB=1`): actual DNS resolution, TLS certificate validation,
HTML extraction and the shared HTTP client used for providers and Telegram.
Go vet, formatting and diff checks pass, as do CGO-free builds for Android/arm64,
Linux/arm64 and FreeBSD/OpenBSD/NetBSD amd64.

New regressions cover corrections during model requests, tool execution and
approval waits; stale-tool skipping; final-response races; attachment and
Telegram update deduplication; queue and step limits; durable restart/resume;
failed-persistence rollback; concurrent terminal input; HTML extraction,
Unicode pagination, binary/attachment rejection, bounded response/expanded text,
public-address/redirect rules, cancellation and Termux resolver configuration.

The live HTTPS test exposed and verified a fix for the pure-Go Termux resolver:
all clients now use the installed Termux DNS and CA paths. No public DNS server
was hardcoded and TLS verification remains enabled.

The disposable binary smoke passes setup, private configuration, workspace guide
creation/preservation, idle reflection, one-shot tasks, soul fallback, shared FTS
recall, SQLite integrity, persistence and resume. Its small text workload used
**21,496 KiB RSS**, not a peak bound. Provider and Telegram protocol tests use
fixtures; no live model or Telegram account was used.

Version 0.7 is installed at `~/alina-poc/bin/alina`, retaining 0.6 as
`~/alina-poc/bin/alina-v06-c1337756`. Android SHA-256:
`c4f87ce3655c9acc00ed188cc9ac1f642c3f511f5354b5722ad7e85f991018b1`.
No packages, services or permanent account configuration were changed.

MarkItDown 0.1.7 is documented as optional. Its ONNX Runtime dependency has no
compatible distribution in the Galaxy's pip index check; conversion itself is
not claimed as tested or available on Termux. See [behavior and sources](web-steering.md)
and the [actual rendered prompt](prompt-example.md).

## Architecture and prompt review — 0.6

All **72 top-level tests** pass with the macOS ARM64 race detector and natively
on Galaxy A15 Termux. Go vet, formatting and diff checks pass. CGO-free builds
pass for Android/arm64, Linux/arm64 and FreeBSD/OpenBSD/NetBSD amd64.

The new regression tests cover queued cancellation ordering, failed approval
persistence, stale approval decisions, current requests/images during compaction,
oversized unanswered inputs, synthetic checkpoint provenance, capability-dependent
prompts, bounded intention indexes, soul delimiters, auxiliary OpenAI research
budgets/usage, malformed or truncated provider output and initiative directories.

The disposable binary smoke passed setup, private configuration, focus view,
idle dream, one-shot scheduling, soul fallback, FTS recall, persistence and resume.
Its small text workload used **16,840 KiB RSS**, not a peak-memory bound. Live
provider/Telegram accounts remain untested; all protocol calls used fixtures.

Version 0.6 is installed at `~/alina-poc/bin/alina`, with 0.5 retained as
`~/alina-poc/bin/alina-v05-0d917588`. Installed Android SHA-256:
`c133775651e7a3c44686637f55e06cdae53a07cf6deb841408aba19d1b08d332`.
No package, service or permanent account configuration was changed.

See the [review and pinned source comparison](review-2026-09-08.md) and the
[rendered prompt example](prompt-example.md).

## Native read/write/edit and prompt guidance — 0.5

All **60 top-level tests** pass with the macOS ARM64 race detector and natively
in Galaxy A15 Termux. Go vet, formatting and diff checks pass; CGO-free builds
pass for Android/arm64, Linux/arm64 and FreeBSD/OpenBSD/NetBSD amd64.

New tests exercise the agent loop using all three file tools, line/byte-limited
pagination, Unicode and CRLF/BOM preservation, exact batch edits, ambiguous and
overlapping match rejection, explicit empty writes/deletions, existing permission
bits, symlink targets, administrative and initiative path checks, cancellation,
external-change detection, large/special files and concurrent independent edits.
The permission fixture explicitly sets its initial mode so Termux's stricter
creation mask does not change what the test is verifying.

The English prompt adds short tool-selection and path guidance. Ordinary turns
have eight tools; reflection keeps four. There is no new runtime dependency or
agent loop. Native file writes are serialized within the daemon, but do not lock
out external editors or shell commands; the documented final race remains.

The disposable binary smoke passed setup, version, Astra defaults, the 95%
threshold, private state, memory, idle dream, scheduling, restart and resume.
Its small text workload used **17048 KiB RSS**; this is not a peak-memory bound.
Model/Telegram tests use fixtures. No live model call, package installation,
service change or permanent account configuration was performed.

Version 0.5 is installed at `~/alina-poc/bin/alina`, with the prior binary retained
as `~/alina-poc/bin/alina-v04-e621f8ad`. Installed Android SHA-256:
`0d917588d2cc8d7fb29192738bd44e6d0fe2fae7edf59a8df4d90a9fb29120db`.

## Astra, English internal writing and Telegram attachments — 0.4

All **52 top-level tests** pass on macOS ARM64 with the race detector and natively
on the Galaxy A15 in Termux. Go vet, formatting and diff checks pass. CGO-free
builds pass for Android/arm64, Linux/arm64 and FreeBSD/OpenBSD/NetBSD amd64.

New coverage verifies dedicated OAuth credential reuse for Astra and hosted
search, request effort/verbosity, encrypted reasoning/phase replay, provider
usage, legacy configuration and seed-soul migration, the strict 95% compaction
boundary, retention after checkpoint failure, stable context prefixes and
exclusion of runtime snapshots from the event archive after re-import.
Reflection uses high effort while checkpoints remain low.

Attachment tests cover the largest Telegram photo flowing through download,
job persistence, Responses image input and a `view_image` tool round trip; files
read by the shell; duplicate updates after restart; completed receipt reuse;
JSON filenames that cannot collide with receipt metadata; unchanged originals;
owner/private-chat checks; declared and streamed size limits; unsafe paths and
redirects; transient download retries and partial-file cleanup; bounded visual
input; missing/changed images; workspace escape rejection; references retained
after compaction; and explicit limitations on nonvisual provider adapters.
Protocol endpoints use local fixtures, not real Telegram/OpenAI accounts.
Native testing caught Android denying hard links; storage now uses exclusive
file creation and an atomic receipt after the download is complete.

The disposable Galaxy binary smoke passed setup, default Astra settings,
272000-token context and 258400-token threshold, English seed soul, private
configuration, memory and schedule operations, expected missing-login failure,
restart and resume. Small-workload RSS was **17412 KiB** with the final binary.
This is a small text workload, not peak memory during image input. No live inference,
hosted search or Telegram delivery was exercised; no dedicated Alina login was
present in the checked default state locations. No packages or services changed.

Version 0.4 is installed at `~/alina-poc/bin/alina`; the previous binary is kept
as `~/alina-poc/bin/alina-v03-789d15b5`. Installed Android SHA-256:
`e621f8add06c63f3bffff621476f9c557931c7c65323667a3fd3e43bcfd70536`.

Context capabilities are based on the local Codex 0.153.4 catalog fetched on
2026-09-08: default 272000, maximum 872000. This does not establish universal
Plus/Pro access or a tested large-context workload. See [Astra details](astra.md).

## Shared archive and reflection — 0.3

The 0.3 update uses one model/tool loop for chat, scheduled work, initiatives and
reflection. An append-only shared archive replaces calendar-based memory
transfers; small notes have decaying attention and explicit pins. Context
checkpointing responds to estimated request size independently of dream.

Validation on the final runtime:

- **39 top-level tests** pass with the macOS race detector and natively in Android
  ARM64 Termux on the Galaxy A15. Go vet, formatting and diff checks pass.
- Native tests cover legacy archive migration and idempotency, shared context
  across channels, retrieval of old events, FTS5, optional semantic note search,
  attention renewal without automatic feedback, pin budgets, corrections,
  no-op reflection, concurrent new events during reflection, soul edit conflicts,
  context-pressure checkpointing and existing approvals/provider fixtures.
- The binary smoke passed interactive setup, private configuration, workspace and
  focus view creation, idle dream with zero model calls, one-shot task lifecycle,
  soul fallback, FTS search/source reads, daemon restart and explicit resume.
- Android/arm64, Linux/arm64 and FreeBSD/OpenBSD/NetBSD amd64 builds use CGO=0.
  FTS5 is registered from the existing go-sqlite3 extension package; there are no
  additional direct dependencies or native SQLite package requirements.

The Galaxy smoke used disposable state and no real model credentials. Its small
workload measured **17020 KiB RSS**. This is not a sustained-load bound, and fixture
results do not establish real-model learning or recall quality. No device packages,
service configuration or permanent account configuration were changed.

Version 0.3 is installed at `~/alina-poc/bin/alina`; the prior binary is retained.
Migration preserves old data and files, but older binaries must not open 0.3
state. Restore a corresponding state backup when rolling back.

## Continuity and personal exploration — 0.2

The 0.2 update keeps Go, the same four tools and the same direct dependencies.
It adds per-session ordering with independent job progress, foreground model
priority, durable session checkpoints and explicit resume, typed/correctable
search across recent and archived memory, source pagination, a personal
workspace, persistent intentions and budgeted one-shot exploration. Dream can
consult evidence before recording lessons or revising the soul.

Validation covers approval waits without blocking independent sessions, ordered
follow-ups, context compaction including a large tool exchange, preserved
transcripts after checkpoint failure, legacy job migration and unknown outcomes
on resume, complete Unicode paging, corrections, soul fallback, one-shot
deduplication, persisted daily budgets, reflection retrieving archived evidence,
and explicit installation declarations under strict policy.

The full macOS race suite contains **34 top-level tests**. `go vet`, formatting,
diff checks and Android/Linux/BSD cross-builds pass. Native Android tests and
a disposable binary smoke exercise setup with autonomy configuration, workspace
creation, empty dream, one-shot lifecycle, recent search/source reads, invalid
soul fallback, job persistence and resume across daemon restarts.

No real model account was used: the smoke expects a missing-login error, and
model/reflection behavior is tested with fixtures. These tests establish runtime
behavior, not the quality of a real model's initiative or learning. The initial
0.2 smoke measured 15,876 KiB RSS; this remains a small-workload observation.

The updated binary remains at `~/alina-poc/bin/alina`; personal exploration is
configured through setup and is disabled until enabled. Old JSON jobs are
retained as migration backups; ongoing job persistence now uses SQLite. No
package installation, service activation or permanent account configuration was
part of this update.

## Original 0.1 baseline


Repository: public `frenchfaso/Alina`, MIT. Built with Go 1.27.1; minimum Go
version in the module is 1.26.0.

### Executed successfully

- macOS ARM64: `go test -race ./...`, `go vet ./...`, formatting and diff checks.
- Galaxy A15 through Tailscale, native Android ARM64 / Termux: all 22 top-level
  tests passed, including SQLite, memory/dream, scheduler, OAuth refresh fixtures,
  provider protocol fixtures, search, Telegram, shell filtering and permissions.
- The actual Android binary, in a disposable configuration directory:
  interactive setup with memory enabled; private config mode; daemon startup;
  Unix socket status; schedule creation/pause/resume/removal; empty dream without
  model calls; soul and diary reads; expected missing-login failure; shutdown;
  restart with persisted memory. Temporary test state was removed afterward.
- Cross-builds with `CGO_ENABLED=0`: Android/ARM64, Linux/AMD64, Linux/ARM64,
  FreeBSD/AMD64, OpenBSD/AMD64 and NetBSD/AMD64. BSD runtime was not tested.

The native suite checks that unauthorised curl and Python socket connections
fail, approved curl succeeds, cancelled subprocess groups terminate, grants have
the requested lifetimes, Telegram rejects other users and duplicate updates,
invalid dream sources retain the original journal, archiving uses the correct
calendar cutoff, embedding spaces are isolated, and scheduled catch-up does not
replay every missed tick. The scheduler test also covers a DST transition.

Measured on the Galaxy A15 after the small binary smoke test: **16,636 KiB RSS**
for the daemon, approximately 16.2 MiB. The stripped Android binary is about
14 MiB. These are small-workload measurements, not steady-state memory bounds
or performance benchmarks for large histories/real inference.

### Installed on the phone

```sh
~/alina-poc/bin/alina setup
~/alina-poc/bin/alina serve
# In a second Termux terminal:
~/alina-poc/bin/alina chat
```

The final executable, native test binary, smoke script and service installer live
under `~/alina-poc`. No system packages were installed, no existing service was
changed, and no permanently running Alina instance was left behind. After setup,
the existing Termux service supervisor can run Alina with:

```sh
sh ~/alina-poc/install-termux-service.sh ~/alina-poc/bin/alina
sv up alina
```

### Account coverage at the 0.1 baseline

No user credentials were supplied for that original run. ChatGPT login, live
OpenCode Go inference, live searches, Telegram delivery and actual embedding
quality were therefore unverified at that point. Protocol tests used fixtures,
including deterministic embeddings for semantic ranking. Later live checks are
recorded above; this historical limitation is not the current account status.

See [operational limits](poc.md) and [research/design notes](research-memory.md).
