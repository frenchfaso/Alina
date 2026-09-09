# 0.10.2: ChatGPT setup response parsing

The first live Galaxy A15 setup exposed a Responses streaming mismatch: the
ChatGPT backend sent a complete assistant item in `response.output_item.done`,
then a successful `response.completed` envelope with an empty `output` array.
Alina authenticated successfully but discarded the completed item and reported
`provider returned no answer or tool calls`.

Retain completed items by output index and use them when terminal output is
empty or omitted. A nonempty terminal output remains authoritative, avoiding
duplicate messages or tool calls. Preserve raw reasoning, phase, function calls,
citations and terminal usage. Still require successful stream completion.

Validation: Responses regression tests cover empty/omitted output, ordering,
full terminal output, truncation, failure and incomplete status. Full macOS race
suite and Go vet pass. On Galaxy A15, a live request through Alina's own Termux
HTTP transport returned ALINA_OK; OpenAI hosted web search also returned content
using the existing ChatGPT login. No Telegram messages were sent, and user/family
configuration was not changed. A standalone diagnostic using Go's default HTTP
client failed on the phone; the application transport passed both live checks.

Protocol reference:
https://developers.openai.com/api/reference/typescript/resources/beta/subresources/responses/methods/create
