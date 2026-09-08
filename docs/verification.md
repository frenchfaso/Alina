# POC verification — 2026-09-09

## Minimal CLI and operational logs — 0.9

The full suite passes with the macOS ARM64 race detector and Go vet. Formatting
and diff checks pass, with CGO-free builds for Android/arm64, Linux/arm64 and
FreeBSD/OpenBSD/NetBSD amd64. Native Termux verification uses the same suite,
including the opt-in public HTTPS test. Provider/OAuth/Telegram checks use
fixtures, with no live account calls or Telegram messages.

New regressions cover removed command aliases, stdin chat and JSON API errors,
redacted configuration round-trips, atomic validation/repair, daemon-lock
exclusion, live-check OAuth exclusion, Telegram webhook conflicts, private
correlated events, HTTP error classifications, concurrent log rotation, log
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
`6a59f122c05bc5b04fdd1d427bf9406d1a00fe2f28d622741c266e9c0d4e0e4b`.
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

## Account-dependent validation still needed

No user credentials were supplied. ChatGPT login, live OpenCode Go inference,
live searches, Telegram delivery and actual embedding quality therefore remain
unverified. Protocol tests use fixtures, including the semantic-ranking test's
deterministic embeddings; they do not measure the quality of a real model's
memory, reflection or retrieval.

`alina setup` configures each integration, including guided Telegram pairing.
After configuration, `alina doctor --live` tests the selected model, configured
search providers, bot token and embedding endpoint. It uses the corresponding
accounts' normal usage and does not send Telegram messages. Send a private
message to the configured bot to verify complete Telegram delivery.

See [operational limits](poc.md) and [research/design notes](research-memory.md).
