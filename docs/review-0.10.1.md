# General review — 0.10.1

Reviewed baseline: `31abb4c` (0.10.0). Scope: setup/configuration, people and
families, daemon lifecycle, agent loop and compaction, steering/permissions,
provider adapters, Telegram, file/web tools, memory/reflection, scheduling and logs.

The architecture remains appropriate for a small device operator. Families add
separate state stores, while retaining one turn implementation, one process,
shared inference arbitration and a single global dream/soul. No new prompt
layer, tool, command, dependency or background timer was needed.

## Reproduced and corrected

| Area | Failure on the baseline | Correction |
| --- | --- | --- |
| Local identity | Default local tasks stored owner `local`. Changing `local_user` attributed the old commitment to the new person. | Native local requests capture the stable user ID. Historical `local` records stay unattributed; native-family mode disables unbound schedules when due instead of assigning them to someone else. Recreate them under an explicit person if needed. Legacy single-user mode still works. |
| Soul recovery | Per-family caches could disagree when soul files became unavailable. A missing primary file at restart replaced a valid backup with the seed. | Scopes delegate soul reads to the global cache/lock. Startup restores a valid backup. Revisions update the cache and backup immediately; an older backup cannot replace the latest known in-process orientation. |
| Telegram pairing | `/start CODE` followed by the person's first message in the same batch acknowledged and discarded that message. | Preserve subsequent messages from the newly paired account in the existing setup inbox. Runtime still verifies configured membership before executing them. |
| Telegram input | A command reply receiving permanent HTTP 400/403 trapped the incoming update stream, blocking another person. | Log the permanent reply failure and checkpoint the incoming update. Transient errors retry; completed jobs retain their separate durable delivery receipts. |
| Diagnostic repair | `doctor --fix` followed a directory symlink before changing SQLite file permissions outside the intended state directory. | Validate every parent inside the instance before inspecting/repairing known files. SQLite diagnostics also refuse directory symlinks. |

Five new top-level regressions failed on the baseline before the fixes. Soul
coverage includes live fallback, restart and a stale disk backup. The local
identity test changes the default person after persisting a task. Telegram
fixtures include a blocked recipient followed by another person's command. The
diagnostic test verifies that an external file's permissions remain unchanged.

## Flow and simplicity

An incoming request resolves its person and scope, then persists a job. The
same loop assembles the operating contract, tools, soul and runtime snapshot;
it alternates inference and tools, incorporating steering at safe boundaries.
Compaction remains pressure-driven at 95%. Completion is persisted before
delivery. Families share archived experience while retaining distinct pending
approvals and reply destinations.

Dream uses that loop with restricted tools and its bounded budget. It can
revisit active family archives while keeping private interpretations in the
global store. General orientation belongs in the shared soul. This review
changes recovery/routing code, not personal soul content or the prompt contract.
Native scope checks and model discretion remain distinct; the shell and
administrative socket are intentionally trusted interfaces.

No new dead-code removal was justified by the reference scan. `Unwrap`, for
example, is used through Go's error interface. Existing primitives for turns,
atomic writes, routing, permission matching and deadlines remain useful.
Generalizing them further would add more machinery than these fixes require.

## Test readiness

The phone's default Alina directory had no configuration or ChatGPT login at
review time. Technical tests use disposable instances and fixtures; the live
phase requires `alina setup` on the Galaxy (or another configured `ALINA_HOME`).
Secrets should be entered in the setup terminal, not in chat.

After setup, `alina doctor --live` checks the selected provider/search and other
configured connections without sending Telegram messages. Actual conversations
should cover a local task, steering/cancel/resume, an image and document, shared
family recall, and a quiet reflection. Discretion, retrieval and learning quality
require real interactions; fixtures establish the mechanics.

Execution results are recorded in [verification.md](verification.md).
