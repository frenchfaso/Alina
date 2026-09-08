# Memory design notes — 0.3

The active design separates recorded experience, selected attention, working
context and reflection. There are no day/week/long-term lifecycle boundaries.

- Keep the event archive and source provenance. Compression is a view used to fit
  the working context; it is not deletion or evidence of a new durable fact.
- Keep a small set of notes in focus. Recency decays continuously; deliberate
  recall or confirmation renews attention, and a few explicit pins remain present.
  The initial half-life is 30 days. Its adequacy needs real-use evaluation.
- A common archive gives continuity across channels. Conversation identifiers
  order current work and route replies; they never restrict recall by source.
- Reflection is a scheduled use of the same agent loop. It may investigate,
  reconsider methods, create an intention, or revise soul. A no-op is successful.
- Do not reinforce a memory merely because the runtime selected it. Do not turn
  transient tool failures into permanent constraints or untested steps into skills.

Inspiration, not claims of equivalent behavior:

- [Hermes memory](https://hermes-agent.nousresearch.com/docs/user-guide/features/memory):
  small curated notes, separate searchable conversation history.
- [Hermes background review, inspected revision](https://github.com/NousResearch/hermes-agent/blob/b2aa855b626ff8688eb34b95c60ee8b6a4af3679/agent/background_review.py):
  reuse the agent loop for reflection. Alina does not add Hermes' post-turn forks
  or separate Curator; its existing dream is sufficient for this POC.
- [Generative Agents, Park et al.](https://arxiv.org/abs/2304.03442):
  an experience stream, selective retrieval and higher-level reflection. Alina's
  decay is a small engineering heuristic, not a replication of that architecture.
- [Anthropic: effective context engineering](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents):
  treat finite working context separately from persistent notes and source access.

Runtime tests cover preservation, recall, correction and budgets. They do not
establish that a chosen model learns reliably or selects the best memories.
