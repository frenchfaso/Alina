# Review of harness awareness and Telegram controls — 0.14

Reviewed commit: `6118aba`, with particular attention to the changes introduced
by `3fd387f` and `6118aba` after the daemon lifecycle review. This is a review
report, not a record of applied fixes. Production code was unchanged when this review was written.

Follow-up in 0.14.1: Telegram resume was removed at the user's request, and stop
now cancels the exact set of that identity's active/queued jobs. The three other
findings were corrected with modality-aware context estimates, dispatch-time
selection checks and cache snapshots readable during refresh. The findings
below describe the reviewed baseline, not the corrected implementation.

## Confirmed findings

### P2 — Resume can select work that has already been resolved

`telegram_controls.go:167` selects the most recent failed, interrupted or
cancelled job without checking whether another job already resumed it. Ordinary
resume does not persist a successor link in the original job. A failed job A,
followed by a successful `/resume` producing B, remains eligible for the next
`/resume`. The second command creates C with the same original intention.
Repeated commands can also queue duplicates while B is running.

A temporary regression probe reproduced two model calls for the same failed
original after the first continuation had completed. The stable Telegram update
ID protects retries of one update, but not separate `/resume` commands.

Persist the predecessor/successor relationship when creating a continuation and
exclude originals that have a successor from implicit selection. Follow the
latest unresolved continuation; keep explicit `/resume ID` behavior deliberate.
The existing restart continuation and submission transaction provide mechanisms
that can be reused without another scheduler or task abstraction.

### P2 — Text-only models are charged for images they never receive

`continuity.go:161` unconditionally includes an image allowance in context
estimation. The new vision flag removes visual input only later, in the provider
adapter. Compaction and its retained-input checks therefore budget a different
request from the one actually sent.

With the existing 8,192-token text-only catalog fixture, a short request asking
for an attached image's filename fails with `current request and visual inputs
exceed available context`, before any provider call. The same class of mismatch
can trigger unnecessary checkpoints in longer conversations.

Estimate the selected model's input projection, retaining attachment metadata
but omitting visual cost when no visual input is sent. Preserve the original
attachments and transcript for future recall or a switch back to a vision model.
Apply the same projection to compaction checks and `/status` estimates.

### P2 — A saved model/effort change can miss the next provider request

`loop.go:111` resolves the model before compaction and before waiting on the
shared inference gate. `continuity.go:80` can then wait behind another person's
inference, but never revalidates that selection. Changes accepted during that
wait do not reach the next outgoing request, contrary to the menu confirmation.

A probe held the inference gate, queued a request, saved a different personal
model, and released the gate. The outgoing request still selected Astra.
Changes during checkpoint generation have the same timing gap.

Validate preference freshness at dispatch. A model change must also revalidate
tools, vision and context, so refreshing only the provider's model ID is not
sufficient. Use one coherent request preparation boundary and repeat preparation
when the preference changed while waiting; do not introduce another agent loop.

### P2 — Catalog refresh blocks cached readers across people

`reasoning_catalog.go:103` holds the shared cache mutex through authentication,
the HTTP request and the cache write. Even `models(ctx, false)` waits behind
that network operation. Every ordinary turn reads through this path.

A probe began a stalled refresh with valid stale metadata already cached. A
cached-only reader could not return until the network request was released.
Thus opening `/model` can delay other conversations and `/status` despite a
usable cached snapshot. Mutex acquisition itself does not honor cancellation.

Keep cached snapshots readable during refresh. Deduplicate refresh writers
separately, perform network I/O outside the snapshot lock, and recheck the
account/catalog identity before publishing the result. No periodic timer or new
dependency is needed.

## Architecture and prompt assessment

The main design still fits Alina: one agent loop, shared inference scheduling,
scoped archives and a single global soul/dream. Native Telegram controls do not
invoke a second agent or add a prompt layer. The embedded harness manual is read
on demand. Configuration remains immutable during execution; staged changes and
one temporary restart helper provide a bounded lifecycle transaction.

The new complexity is concentrated at request boundaries, not in the agent's
reasoning flow. Simplify those boundaries rather than rewriting the architecture:

- Resolve effective model, effort, vision and context together. Selection and
  normalization currently span the catalog, loop, inference wrapper and provider;
  `/status` also duplicates context-budget logic.
- Reuse one input projection for transport capabilities and context estimation.
  Keep the source transcript richer than the outgoing request.
- Reuse continuation persistence for both ordinary resume and controlled restart.
- Keep the system prompt stable and capability-specific. There is no reason to
  add model catalogs, setup instructions or a longer self-description to it.

No material new dead-code problem was identified in these additions. The useful
cleanup is duplicated decisions and state transitions, not extra interfaces,
a plugin framework or a separate planning subsystem.

## Verification and limits

The existing full macOS ARM64 race suite passed: 165 top-level tests passed,
one optional public-web smoke skipped, zero failures. This includes native
subprocess restart, rollback, delivery/idle draining, continuation idempotency,
configuration conflict checks, family boundaries, steering and provider fixtures.
Go vet and diff checks passed.

Four additional temporary Go-overlay regression probes each failed at the
expected assertion, confirming the four findings above. The probes used isolated
state and fake providers/Telegram transports; they did not change production
source. The suite's green result is therefore not evidence that these uncovered
cases are handled.

No Galaxy A15 changes, live Telegram messages or account-backed inference were
performed. Real catalog availability and switching models with existing opaque
provider history still need an account-backed integration check; this review
does not claim those live behaviors are verified.

The [official reasoning guide](https://developers.openai.com/api/docs/guides/reasoning#change-reasoning-mid-conversation)
also describes Astra configuration updates that preserve the request-level
reasoning prefix for caching. That is a potential later optimization, not a
confirmed defect here: compatibility with the ChatGPT subscription backend must
be established before adding another kind of history item.
