# 0.10.3: quieter Telegram conversations

Normal messages and steering no longer generate submission acknowledgements.
Successful replies contain only Alina's output, without job IDs or status
prefixes. Empty successful outputs are acknowledged in the delivery store
without sending an empty placeholder. Approval buttons, failures, interruption
notices and instructions to resume pending steering remain visible. Explicit
commands such as /status retain diagnostics; no queue or log format changes.

Validation: full macOS race suite, focused delivery regression coverage and
Go vet. Existing multi-person routing and delivery-backlog tests now check
plain replies while retaining attribution and restart receipt coverage.
