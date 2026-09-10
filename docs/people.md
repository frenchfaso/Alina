# One family, one Alina

Alina recognizes each person and shares memory across the family, including her
own dream history. Telegram accounts remain separate conversations with distinct
reply destinations; a Telegram group is not required. One soul, one reflection
schedule and one inference gate serve everyone.

## Setup

Stop the daemon and run `alina setup telegram`. Pair a dedicated bot, give the
first person a name, then add others through single-use pairing links. There is
no family question: new users join the default person's family. Repeat the
command to add, rename or remove people. The first person is the default for
local chat; `local_user` can be changed through `alina config apply`.

All configured people are trusted device operators. Telegram's authenticated
numeric ID identifies the person; names or claims in chat never grant access.
Memory sharing does not make people's preferences or requests interchangeable.
Messages, files and approval requests still go to the requester. `/stop` affects
that person's work, not everyone else's.

A configuration example (IDs are placeholders):

```json
{
  "users": [
    {"id":"alex", "name":"Alex", "telegram_id":111, "family":"home"},
    {"id":"bea", "name":"Bea", "telegram_id":222, "family":"home"}
  ],
  "local_user":"alex"
}
```

A patch replaces the entire `users` array. IDs are stable ASCII labels; names
can change. The POC permits 32 people. Keep the existing `family` value when
editing users: it also identifies the historical archive location.

## Remembering dreams

The ordinary `memory` tool searches both the family archive and the global mind.
`read part=dreams` lists attempts, outcomes and final reflections, newest first.
Read a returned source ID for a message, or a job ID for its complete transcript;
long reads return `next_offset`. There is no separate diary or public/private
copy. Interpretations remain distinct from verified observations.

Dream starts with a compact, deterministic batch of new conversation excerpts
and tool names. Full text and tool output remain accessible by ID. Existing
cursors advance only through the represented batch, after a successful turn;
failed batches and concurrent or later events remain eligible. No additional
model summarizes the archive. Context compaction remains separate.

`harness status` and `alina status` expose the last attempt/outcome, most recent
completed reflection and next scheduled time. A successful job with nothing to
reflect on is reported as `skipped`, not as a completed reflection.

## Existing installations

Original SQLite databases, transcripts, workspaces and grants stay in place.
The global mind remains under `$ALINA_HOME/mind`; the family's archive may live
at the state root or under `scopes/`, as recorded by `people-state.json`. Shared
retrieval reads these existing stores without copying records or changing IDs.
A legacy single personal owner can become the first member of a family: its
binding is updated on startup while its archive stays at the original path.
An existing destination archive is never overwritten or silently merged.

Older installations with multiple family/personal scopes retain their original
boundaries and private global reflection. The simplified wizard does not create
new families. Other legacy scope IDs and local API selectors remain for archive
compatibility; inactive histories stay on disk. Do not delete the binding file.

Pairing preserves pending updates from authorized users. Removed users' scheduled
tasks are disabled when next due; replies are not reassigned. Unattributed legacy
`local` records stay unattributed, even if the default local person changes.

## Local administration

```sh
alina api GET /v1/people
alina api GET '/v1/memory/read?q=dreams'
alina api GET '/v1/memory/read?q=JOB_ID&offset=16000'
alina api GET '/v1/memory/search?q=reflection'
alina api GET '/v1/status?scope=global'
alina api POST /v1/memory/jobs '{"kind":"dream"}'
```

`?user=ID` selects the local person's identity; `?scope=...` selects an existing
archive for administration. A requested dream always runs globally. The Unix
socket and shell run under the same trusted OS account. `doctor` checks all stored
scope databases. Configuration credentials and identity bindings still require
local setup/config; autonomous dream cannot change them or restart the daemon.
