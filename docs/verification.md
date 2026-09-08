# POC verification — 2026-09-08

Repository: public `frenchfaso/Alina`, MIT. Built with Go 1.27.1; minimum Go
version in the module is 1.26.0.

## Executed successfully

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

## Installed on the phone

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
