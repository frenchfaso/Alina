# Bounded workers

Alina can delegate substantial web research, document analysis and file work
without carrying every intermediate result in the conversation. Quick lookups
and small operations stay with Alina. Calendar stays with Alina too.

`delegate capabilities` reads the cached ChatGPT catalog. `start` takes a clear
task, necessary context, an optional supported Sol 6.1 reasoning level and optionally files.
Only that material enters the worker's context: no conversation history, soul,
memories or private harness state. Sol 6.1 must be available in the account;
there is no silent model/provider substitution. Workers default to medium
reasoning; Alina may choose a lower or higher supported level for the task.
Capabilities include the default, available levels and budgets; consult them
when selecting a non-default effort. A default delegation can start directly.

One worker may run at a time across the device, alongside the main inference
loop. It uses the same agent loop with a smaller tool set and a separate trace.
The parent receives a report automatically before finishing its turn. Incoming
user messages can still steer Alina. `wait` sleeps until the report without model
calls; steering and cancellation wake it. Alina does independent work rather than
repeating delegated research or polling status. She can also inspect status or
cancel the worker.
Stopping the parent also stops the worker. Restarted workers are marked
interrupted and never replayed or resumed automatically.

The configured `max_steps` bounds work requests (default 40). At the boundary,
one extra tool-free request produces a concise partial report with verified
results and unresolved work; `partial: true` is recorded in job status and logs.
Workers have a ten-minute deadline, with the last minute reserved for this report.
Cancellation still stops everything. Provider failures can prevent synthesis;
the saved trace remains available. Reports are bounded to about 4k bytes.

Workers use the model context window, capped by configured `context_tokens`, and
the same automatic 90% compaction as Alina. There is no special 64k ceiling or
cumulative token cutoff: repeated/cached input is usage, not occupied context.
Usage is still logged. Checkpoint archives stay beside the private worker trace.
The model sees remaining work requests and working time as ephemeral context.
`delegate trace` pages through the saved, bounded exchanges only when
Alina needs details; worker exchanges are not added to the memory journal.

## Tools and files

Workers have `read`, `write`, `edit`, `web_search` and `web_fetch`. They cannot
use Calendar, memory, soul, configuration, scheduling, direct user delivery or
another worker. These are dispatch restrictions, not just prompt instructions.

Up to 32 regular files (32 MiB total) can be copied into a dedicated workspace.
Names must be unique. File tools stay inside it, including through symlinks.
Alina reviews and applies resulting artifacts; the worker does not directly
edit originals. Workspaces remain under `workspace/delegates/ID`, and traces
under the memory scope's `delegates/ID/trace.json` for inspection.
If a start fails before registering the worker, its newly created workspace is
removed. Workspaces and traces for workers that started remain available,
including unsuccessful or interrupted work.

Offline shell is offered only with OS filesystem isolation: Landlock ABI 3+
and seccomp on supported Linux/Android architectures, or sandbox-exec on macOS.
Other systems retain file and research tools. The Galaxy A15 tested on
2026-09-26 did not expose the required Landlock support, so its workers have no
shell. Alina's ordinary shell remains available. This is not a container,
a resource-quota system or an isolation boundary for hostile local users.

## Web research

Hosted OpenAI search defaults to `gpt-6-sol` with `high` reasoning, independently
of the conversation or worker model. An explicit `search.openai_model` replaces
the default model. Search gets only its query and a small research prompt.

In a direct user conversation Alina can override `model` and optionally
`reasoning` for one `web_search` call when requested or justified. Overrides
are checked against the cached ChatGPT catalog and require that same ChatGPT
authentication route. Omitting effort chooses high when supported, otherwise
the selected model's catalog default. Overrides never modify saved settings.
Workers always use the configured search defaults. Tavily and Brave retain
their existing key-based search paths and have no model/effort controls.
