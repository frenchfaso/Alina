# People, families and one global mind

Alina has one personality, one `soul.md` and one scheduled dream. Conversations
belong to native memory scopes: a family shares an archive; an unaffiliated
person has a personal archive. Telegram groups do not define membership.

## Setup

With the daemon stopped, run `alina setup telegram`. Connect a dedicated bot,
give the first person a name and an optional family ID, then add other people
through single-use pairing links. Matching family IDs share memory. A blank
family means personal memory. Running the command again offers add/edit/remove.
The first person is also the default identity for local chat. New local jobs and
tasks capture that person's stable ID; changing `local_user` does not reassign
them. Old records with owner `local` have no recoverable person binding and stay
unattributed. In native-family mode, their schedules are disabled when due;
recreate any still-needed task under the intended user. Legacy single-user mode
continues to use `local` normally.

All configured people are trusted device operators. Families separate memory,
not Unix accounts. The wizard says this explicitly. It never adds a person from
a display name or a claim made in chat: Telegram's authenticated numeric ID is
the account binding.

Configuration can also be managed through `alina config apply`:

```json
{
  "users": [
    {"id":"alex", "name":"Alex", "telegram_id":111, "family":"home"},
    {"id":"bea", "name":"Bea", "telegram_id":222, "family":"home"},
    {"id":"chris", "name":"Chris", "telegram_id":333}
  ],
  "local_user":"alex"
}
```

The IDs above are examples, not real account bindings. A patch replaces the
whole `users` array. User/family IDs are stable ASCII labels, up to 40 characters
(letters, digits, `_`, `-`); display names can change. The POC permits 32 people.
An added family member can recall that family's existing history. Moving a
person changes their current scope; it does not copy or relabel old records.
Histories of inactive scopes remain on disk but are not attached automatically.

## Hard boundaries and discretion

Native memory operations, relevance/search, transcripts, attachments and file
tools use the authenticated person's scope. A conversation cannot choose
another scope by passing an ID or a `scope` tool argument. Messages and pending
approvals go to their requesting Telegram account, including scheduled jobs.
Members of a family share memories but retain distinct names, preferences,
active conversations and approval ownership. Durable/restart grants apply to
their scope, for the existing exact command/directory permission contract.

Global dream can explicitly inspect all active scopes, revisit evidence and
update notes in their original archive. Its interpretations and transcript stay
in the private global archive. It can distill general values, methods and
lessons into the shared soul. Prompts ask Alina to use discretion: do not turn
the soul into identifiable stories, quotes, family facts or secrets; do not
transfer confidences into another family's replies or notes. This is a model
instruction, not a deterministic privacy filter.

The shell, configured work directory and administrative local API are shared
on the same OS account. Those paths rely on Alina's discretion and the trust
between configured operators. This design does not claim tenant isolation.

## Small implementation

There is still one process and one turn implementation. Each active scope
reuses that implementation with its own SQLite state, transcripts, workspace,
schedules and grants. A global inference gate prioritizes foreground work.
The daily personal-exploration budget is shared; adding families does not
multiply it. Only the global scheduler creates the built-in dream.

The global state lives in `$ALINA_HOME/mind`; other scopes live under `scopes/`.
The former single-user archive stays at its original path and is bound once in
`people-state.json`. Existing single-user configurations continue to work until
native users are configured. Once that binding exists, removing all users is
rejected rather than exposing family state through legacy `owner_id` mode.
Do not delete or edit the binding to change membership.

Dream snapshots each active archive's latest conversation row and remembers
its cursor after a successful reflection. Concurrent new messages remain
eligible for the next dream. It receives a compact index, not every archive in
the prompt. Context compaction remains separate and driven by context usage.

Pairing persists messages from already authorized people before acknowledging
Telegram updates, then the daemon drains that inbox. If a saved message predates
a family change, Alina asks the sender to resend it into the new scope. Removed
users' scheduled tasks are disabled when next due; old results are not redirected.
Failed delivery to one person does not prevent delivery to others.

## Local administration

`alina chat` and `status` default to `local_user`. The Unix socket remains an
administrative interface, not an authentication boundary:

```sh
alina api GET /v1/people
alina api GET '/v1/memory/read?q=focus&user=bea'
alina api GET '/v1/status?scope=global'
alina api GET '/v1/tasks?user=alex'
alina api POST /v1/memory/jobs '{"kind":"dream"}'
```

Use `?user=ID` for a person's current scope and local identity, or `?scope=...`
for explicit archive administration. A requested dream always runs globally.
Diagnostic agents should select the intended user explicitly rather than assume
the default local identity matches the person requesting help. `doctor` checks
all stored scope databases, including inactive ones, without traversing workspaces.

The `harness` tool follows these boundaries: job status and diagnostic events are
scoped, configuration omits other people and Telegram routing, and in-chat
patches cannot edit identity bindings. Behavior settings remain device-wide and
affect all families. Only user conversations may request changes/restarts; the
global dream can inspect state but cannot change administrative settings.
