# Astra configuration

These are the current global defaults (low effort for all work since 0.15.2). The provider capability figures below
were checked on 2026-09-08 against the official model/Responses documentation
and the local Codex 0.153.4 catalog fetched that day. The subscription backend
is not interchangeable with the public API.

| Setting | Default |
| --- | --- |
| `model` | `gpt-6-astra` |
| `context_tokens` | `272000` |
| Compaction trigger | strictly above 95%, or 258400 tokens |
| `reasoning_effort` | `low` |
| `dream_reasoning_effort` | `low` |
| `checkpoint_reasoning_effort` | `low` |
| Hosted search effort | `low` |
| `verbosity` | `low` |
| `model_timeout_seconds` | `600` |
| `search.default` | `openai` |
| `search.openai_model` | empty: follow the ChatGPT model, otherwise Astra |

The Codex catalog reports `context_window=272000`, `max_context_window=872000`,
`effective_context_window_percent=95`, default reasoning `medium` and verbosity
`low`. Alina uses 272000 by default as requested; 872000 is an explicit opt-in
upper bound for this ChatGPT adapter, not an assertion that every account has
been tested with that much context. The public API model card instead lists
1050000 total tokens, maximum input 922000 and maximum output 128000.

For this verified Astra configuration, supported effort values are `low`, `medium`, `high`, `xhigh`, `max`. Ultra is an
orchestration mode in Codex, not a single-response effort in Alina. Astra function
calling uses Responses. No temperature, top_p, logprobs, paid service-tier
override or backend-unverified output/cache lifetime parameters are sent.
Checkpoint effort is set on a separate request and cache key, leaving normal
turn settings stable. Raw output items, including encrypted reasoning and phase,
are replayed unchanged; usage/context accounting metadata stays local.

The prompt encourages completion of authorized work, reasonable assumptions,
proportional verification, concise replies in the user's language and English
internal writing. Variable context is appended to the working transcript so it
does not rewrite the cached prefix every turn. The implicit cache remains in use;
actual cache counts are recorded when the backend returns them.

Compaction uses measured usage when available plus estimated growth, not a
provider tokenizer. Original messages and encrypted items remain in saved JSON
transcripts; the searchable SQLite archive stores readable events. Checkpoint
input contains readable text, call arguments, results and source IDs, without
opaque encrypted items. Chunk size scales with the configured budget up
to 500000 bytes, avoiding the former fixed 48000-byte sequence of small calls.
The final checkpoint remains bounded to 6000 bytes. A failed checkpoint does not
replace the working transcript. Measurements are invalidated after compaction.

`alina setup --advanced` exposes Astra effort, verbosity and context size.
For other global settings, use `alina config apply` with the daemon stopped;
ordinary quick setup preserves existing choices. `alina doctor` reports the
saved global settings; `doctor --live`, also while stopped, checks the configured
model and selected search provider. Telegram `/status` reports the person's
effective model and effort. See [configuration and diagnostics](operations.md).
Existing ChatGPT configurations using the retired POC `gpt-5.4` default migrate
on load to Astra; its old 32768-token default becomes 272000. Other selected
models and custom context budgets remain unchanged. An old subscription search
default becomes model inheritance; explicit API-key search models are preserved.
Loading does not rewrite the configuration file; setup/save persists it.

OpenAI search reuses the exact same `Auth` object as chat, including refresh and
account headers. A separately configured API key is an explicit public API
override with separate billing; errors never trigger a paid fallback.

## Evidence and limits

- [GPT-6 Astra model card](https://developers.openai.com/api/docs/models/gpt-6-astra)
- [Astra migration and prompting](https://developers.openai.com/api/docs/guides/latest-model?model=gpt-6-astra)
- [Codex model availability](https://learn.chatgpt.com/docs/models)
- [Reasoning continuity](https://developers.openai.com/api/docs/guides/reasoning#preserve-reasoning-across-calls)
- [Prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching)

The catalog is evidence of configured capabilities, not a large-context live
benchmark or universal Plus/Pro entitlement. Request tests use a local HTTP
fixture; real model and hosted-search access still require Alina's own login.
No credentials from Codex or another application are imported.

Telegram `/model` and `/think` now obtain choices from the authenticated provider
catalog, cached per account. Personal selections override ordinary conversation
requests without changing these global defaults or dream effort; see
[model controls](operations.md#telegram-model-and-reasoning-controls).
