# Web reading, steering and document conversion

The single agent loop remains unchanged in shape: prepare context, ask the model,
execute tools, repeat. Steering uses a bounded persistent mailbox at its safe
boundaries; web_fetch uses a Go web reader. There is no second agent, browser
runtime, MCP server, Python service or additional model request for these features.

## Public web pages

`web_fetch` reads public HTTP(S) URLs without saving a file. It accepts HTML,
XHTML, plain text, Markdown, JSON and XML. HTML extraction preserves document
order, headings, lists and link destinations, and omits scripts, styles and
explicitly hidden elements. It does not execute JavaScript or claim full browser
rendering, article detection or exact Markdown table/layout conversion.

Results contain the final URL, title, content type, external-content label and
character offsets. The default page is 12000 characters, maximum 24000. A later
page refetches the URL; a changing page is not a fixed snapshot. Both the decoded
response and extracted text are capped at 2 MiB, with a 25-second request timeout
and at most five HTTP requests in a redirect chain. Oversized content returns an
error rather than an apparently complete excerpt.

This is pre-authorized research, like web_search. Attachment responses and
unsupported binary media require the existing consented shell download path.
There are no custom headers, cookies, credentials or environment proxies. Each
DNS answer is checked before dialing the same IP; local/private/reserved
addresses and HTTPS downgrades are rejected. Use shell under the configured
network policy for local services. Personal initiatives expose web_fetch only
when their existing web research permission is enabled. Dream has memory,
personal scheduling, saved-image inspection when supported, read-only harness
access and soul revision; it cannot fetch pages or run shell commands.

The added parser is Go's `golang.org/x/net/html`, with its charset support. It
does not require CGO. The module graph also updates x/sys and x/term and adds
x/text through x/net's selected versions.

An actual HTTPS test exposed a pre-existing Termux issue in cross-built pure-Go
binaries: Go looks for `/etc/resolv.conf`, while Termux uses
`$PREFIX/etc/resolv.conf`. All Alina HTTP clients now use the configured Termux
nameservers and its installed `$PREFIX/etc/tls/cert.pem` trust bundle. No DNS
provider is hardcoded and TLS verification stays enabled. The DNS file is read
again on new lookups; trust-store changes need a daemon restart. Explicit
`SSL_CERT_FILE`/`SSL_CERT_DIR` settings are respected. Keep `PREFIX` in the daemon
environment. This uses Termux's resolver file, not Android's private-DNS settings
or automatic Tailscale split-DNS discovery. Other operating systems retain their
ordinary resolver and certificate behavior. Termux's own Go package addresses
the same [hardcoded paths](https://github.com/termux/termux-packages/blob/master/packages/golang/build.sh)
at toolchain build time.

## Steering

- Telegram text and attachments, and ordinary lines in `alina chat`, steer the
  oldest accepting chat job with the same conversation and owner. With no such
  job they start a new one. `/new` explicitly changes conversation; it does not
  cancel earlier work. One-shot `alina chat "message"` and piped input use the same steering behavior.
  API submissions with `interactive:false` and scheduled work retain FIFO submission.
- `alina api POST /v1/jobs/JOB_ID/steer` with a JSON message targets a local job. HTTP:
  `POST /v1/jobs/{id}/steer` with `message` and an optional stable `request_id`.
  `POST /v1/jobs` supports `interactive: true` for automatic routing.
- A running model request finishes normally. Before another model call, after
  compaction, between tools and before returning a final answer, the loop checks
  for new input. Unstarted tool calls receive explicit skipped results, keeping
  the provider's call/result protocol valid. Messages enter as actual user input,
  with attachments, and the same objective and transcript remain in context.
- A running shell command finishes normally; subsequent tools are reconsidered.
  Use Telegram `/stop` to cancel all your active/queued jobs, or `/cancel ID`
  and `alina api POST /v1/jobs/ID/cancel` for a specific job. Steering is
  a correction at a safe boundary, not process preemption.
- A pending approval is superseded by steering without granting permission.
  Its old button/ID cannot authorize a later action. The chat uses `/approve
  1|2|3|4` when exactly one approval is displayed, retaining once/restart/always/
  deny. Plain numbers remain ordinary user messages.
- SQLite saves each accepted message before acknowledgement. Request IDs
  deduplicate retries, including Telegram uploads. At most 16 messages of 32000
  bytes each may wait per job. Steering never resets the model-step budget.
- The transcript is persisted before a mailbox item is marked applied. Stable
  archive IDs avoid duplicate journal entries during recovery. Pending items
  survive cancellation, failure and service restart; the explicit local
  `alina api POST /v1/jobs/ID/resume` recovery operation transfers them to the
  resumed job transactionally. Telegram `/resume` is removed. Status reports
  their count. Applied messages remain in the normal transcript/archive;
  unapplied ones remain in the mailbox
  until explicit recovery, rather than becoming a silently executed new request.
- Acceptance and the final empty-mailbox check share a lock. A message racing
  normal completion either continues that job or starts a new queued turn.

The terminal client has one input reader and one event loop. It displays activity
and completed answers, without streaming tokens or a fullscreen TUI. `/quit`
detaches; Ctrl-C cancels the jobs being followed. EOF never grants consent.

The boundary behavior is inspired by the
[Pi agent loop](https://github.com/earendil-works/pi/blob/6160683a4a8012f0d1cd30c145df18b4ca6f5176/packages/agent/src/agent-loop.ts).
Alina keeps its own small durable implementation because its daemon may restart
without an interactive client and must deduplicate Telegram updates.

## MarkItDown

[Microsoft MarkItDown](https://github.com/microsoft/markitdown) is the requested
project: an MIT-licensed Python utility for document-to-Markdown extraction,
including PDF and Office formats. The
[upstream package definition](https://github.com/microsoft/markitdown/blob/main/packages/markitdown/pyproject.toml)
requires Python 3.10+ and supports selective format extras. It is useful for LLM
input, not a promise of faithful visual layout. OCR, transcription, plugins and
cloud processing are separate choices; ordinary local document extraction does
not use Alina's model credentials.

Alina seeds `workspace/procedures/markitdown.md` once and points to it in the
prompt. The guide covers discovery, local conversion, consented virtualenv
installation and platform limitations. Existing guides and procedure indexes are
not overwritten. `alina doctor` reports whether the executable is on PATH.
No new native converter tool or mandatory Python dependency is added.

Verified 2026-09-09 against PyPI: MarkItDown 0.1.7 requires Magika 0.6.x, whose
Python API needs NumPy and ONNX Runtime. On Galaxy A15 / Termux Python 3.14.6,
`python -m pip index versions onnxruntime --disable-pip-version-check` returns
`No matching distribution found`. The current
[official ONNX Runtime distributions](https://pypi.org/project/onnxruntime/#files)
have no Android wheel. Therefore normal upstream pip installation is currently
blocked on this device; no dependency bypass, package install or remote upload
was performed. BSD compatibility is also unverified. Installed format-specific
utilities remain available through shell; Termux provides a
[Poppler package](https://github.com/termux/termux-packages/blob/master/packages/poppler/build.sh)
for PDF utilities, subject to installation consent if needed.
