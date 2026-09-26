# Bounded workers

Alina can delegate substantial web research, document analysis and file work
without carrying every intermediate result in the conversation. Quick lookups
and small operations stay with Alina. Calendar stays with Alina too.

`delegate capabilities` reads the cached ChatGPT catalog. `start` takes a clear
task, necessary context, a supported Sol 6 reasoning level and optionally files.
Only that material enters the worker's context: no conversation history, soul,
memories or private harness state. Sol 6 must be available in the account;
there is no silent model/provider substitution.

One worker may run at a time across the device, alongside the main inference
loop. It uses the same agent loop with a smaller tool set and a separate trace.
The parent receives a report automatically before finishing its turn. Incoming
user messages can still steer Alina; she can inspect status or cancel the worker.
Stopping the parent also stops the worker. Restarted workers are marked
interrupted and never replayed or resumed automatically.

Limits are ten minutes, at most 20 steps, at most 64k estimated context tokens,
and a cumulative measured input/output budget of 200k tokens. The token budget
stops the next request after reaching the limit; a final request can exceed it.
The report is bounded to about 4k bytes. Failures and incomplete work remain
explicit. `delegate trace` pages through the saved, bounded exchanges only when
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
