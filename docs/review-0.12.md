# 0.12: a detached daemon, without autostart

`alina serve` now returns after launching a separate session with stdin detached
and stdout/stderr redirected to a private diagnostic file. The child announces
readiness through an inherited pipe only after initialization and socket setup;
the parent verifies the local API and process identity before reporting success.
Startup failures return to the caller. Interrupted/failed startup terminates
only the child launched by that invocation.

`serve stop` requests graceful shutdown over the private local socket and waits
for the state lock to be released. `serve restart` waits for the old instance
before starting the current executable. An additional short-lived control lock
serializes launch/stop/restart commands. Repeated starts and stops are idempotent.
No PID file is used for signals. Status includes the actual service PID.

Setup's optional final start uses the same detached path. `serve --foreground`
retains the old behavior for debugging and existing service managers. The Termux
runit installer was updated accordingly. No autostart, boot configuration,
package installation or native supervisor registration is performed.

Operational events retain the existing rotating JSONL logger. Startup stderr
and runtime panics go to logs/daemon.log (0600), rotated on a later start when
over 2 MiB. The foreground process keeps stderr attached to its supervisor.

Regression tests launch real subprocesses and verify readiness, survival after
launcher exit, repeated start/stop, restart with a new PID, private output logs,
failed startup without orphaned state locks, and the existing foreground/socket
flow. Platform build and device results are recorded in verification.md.
