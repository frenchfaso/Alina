# POC verification — 2026-09-08

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
