# Implementation references

Alina's code is an original Go implementation. These primary sources informed
the protocol adapters and operating model:

- [Pi](https://github.com/badlogic/pi-mono), inspected at commit
  `4a6ed01945c7f6a2a350996fb439148149ab65ee`: small agent loop, OpenAI Codex
  OAuth device/PKCE flow and Codex Responses request headers/body. Relevant files:
  `packages/ai/src/auth/oauth/openai-codex.ts` and
  `packages/ai/src/api/openai-codex-responses.ts`.
- Pi's [read](https://github.com/badlogic/pi-mono/blob/main/packages/coding-agent/src/core/tools/read.ts),
  [write](https://github.com/badlogic/pi-mono/blob/main/packages/coding-agent/src/core/tools/write.ts)
  and [edit](https://github.com/badlogic/pi-mono/blob/main/packages/coding-agent/src/core/tools/edit.ts),
  inspected on 2026-09-08: line-based pagination and unique, non-overlapping
  replacements against the original file. Alina uses strict byte matching,
  streaming text reads, bounded file sizes and atomic writes; images retain
  their existing dedicated tool. It does not port Pi's fuzzy matching or UI code.
- [OpenAI authentication](https://learn.chatgpt.com/docs/auth): subscription
  versus API access, device login, refresh and credential handling.
- [OpenAI web search](https://developers.openai.com/api/docs/guides/tools-web-search):
  Responses tool and citation annotations.
- [OpenCode Go](https://opencode.ai/docs/go/): model endpoints, client identity
  and stable session header. Access is subject to provider terms and limits.
- [Tavily Search](https://docs.tavily.com/documentation/api-reference/endpoint/search).
- [Brave Search](https://api-dashboard.search.brave.com/app/documentation/web-search/get-started).
- [Telegram Bot API](https://core.telegram.org/bots/api): long polling,
  update offsets, inline keyboards and callback queries.
- [Termux services](https://github.com/termux/termux-services),
  [Termux:Boot](https://github.com/termux/termux-boot), and
  [Android Doze](https://developer.android.com/training/monitoring-device-state/doze-standby).
- [ncruces/go-sqlite3](https://github.com/ncruces/go-sqlite3): bundled SQLite
  without CGO, with a Go virtual filesystem.
- [robfig/cron](https://pkg.go.dev/github.com/robfig/cron/v3): portable cron
  parsing, calendar schedules and timezone handling.
- [OpenAI embeddings](https://developers.openai.com/api/docs/guides/embeddings):
  vectors and the OpenAI-compatible embedding request format.

Memory/reflection papers and the corresponding design choices are in
[research-memory.md](research-memory.md).

Pi, Hermes, OpenClaw and Crush are design references, not runtime dependencies.
The POC has no extension VM yet; shell utilities, small provider interfaces and
separate transport code are the initial extension points.

The [0.6 review](review-2026-09-08.md) compares the current Pi and Hermes prompt,
compaction, memory and loop implementations at pinned source revisions.

The [0.7 notes](web-steering.md) cover Pi-inspired steering, Go HTML extraction,
Microsoft MarkItDown and its Termux dependency limitation, and the verified
Termux DNS/certificate adapter.
