# Harness awareness and controlled self-restart — 0.13

Alina previously knew its tools but had no bundled description of its own
lifecycle. Local configuration writes also required stopping the process, so
in-chat self-configuration could not complete safely.

## Changes

- One native `harness` tool: on-demand manual, state/configuration, scoped local
  diagnostics, staged configuration, discard and controlled restart.
- A software-owned English Markdown reference, embedded in the binary and
  installed in each workspace. Its link is added to the existing procedures
  index; personal procedures are preserved. The prompt replaces the absolute
  administrative-write prohibition with user-requested changes through the tool;
  it does not include the manual.
- Active configuration remains immutable. Partial changes are validated and
  staged in one private transaction, with saved-config conflict detection.
  Account/endpoint/people/Telegram-routing administration stays in local setup.
- One detached helper waits for completed work and the Telegram reply receipt,
  stops gracefully, applies settings, and verifies new-daemon readiness. Failed
  startup gets one configuration rollback/start attempt. No permanent supervisor.
- Same-owner/session continuation with a stable ID and atomically saved outcome.
  Cancelled restarts do not replay the task. Both terminal modes follow the
  continuation across temporary unavailability; Telegram retries incoming updates
  rather than acknowledging a request it could not submit during the drain.
- Mutations are rejected in autonomous work and in restart continuations. A
  pending result cannot be overwritten by another configuration operation.

## Deliberate limits

User authorization remains a model operating rule within the existing trusted
OS account; native dispatch additionally restricts eligible job kinds. This is
not an adversarial multi-tenant administration boundary. Configuration affects
the whole instance, regardless of the requesting family.

Readiness checks local process startup, not provider/model availability. There
is no hot reload, credential editing in chat, automatic model fallback or
permanent watchdog. Foreground/supervised instances use their controller.
If the helper is killed or recovery fails, the private transaction remains for
local repair. It contains configuration credentials and must not be published.

The README no longer describes day/week memory tiers. Setup docs now describe
background launch. The tool catalog and prompt example are current, and older
verification entries are explicitly historical rather than a deployment inventory.
