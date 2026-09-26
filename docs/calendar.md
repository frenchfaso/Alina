# Google Calendar

Ask Alina in Telegram: **“Help me connect my Google Calendar.”**
She gives the remaining sharing steps and a brief, informal explanation of how
event data is used. No Google password,
private iCal link or credential file belongs in chat.

1. In Google Calendar **on the web or in the Android app**, open Settings, select the calendar and
   share it with the service-account email Alina provides. Choose **See all
   event details** for read-only. To let Alina create and edit events on your
   explicit request, grant event editing instead; sharing-management permission
   is not needed. Existing reader calendars remain read-only.
2. Copy the **Calendar ID** from Settings → Integrate calendar and send it to
   Alina. For the primary calendar this is usually your Google email address.
3. Tell her to connect after the brief explanation. Alina verifies event access and saves the
   connection under your configured identity. Explicitly ask to make the
   connection available to other configured family members if desired.

Then ask questions such as “What appointments do I have tomorrow?” or “How
many days until my next appointment?” Each person connects their own calendars.
To remove a connection, ask Alina to disconnect it; revoke sharing in Google
Calendar as well if the service account should lose access entirely.

## Delegate writing to a family member

The connection owner can say **“Let Clearpunch create and edit events in my
calendar”**, or **“Revoke Clearpunch's permission to edit my calendar.”** Alina
resolves the person from configured family members and asks if the name is
ambiguous. Only the owner can grant/revoke, in a direct conversation. Each grant
is specific to one calendar and person; it includes reading that calendar even
without broad family sharing. Delegates cannot manage sharing or re-delegate.

The tool checks that both people still belong to the family in which the grant
was issued. Google must separately grant event editing to the service account.
Revocation removes delegated writing; any ordinary family read access and
historical conversation contents remain. Existing connections have no delegates
until explicitly granted. Delegations survive restart and re-linking.

## Data and limits

- Reads calendars shared as reader, writer or owner. The OAuth scope is
  `calendar.events`; Google's per-calendar sharing permissions still determine
  which calendars can be changed. No deletion or sharing-management tool.
- Creation and editing require a direct request from the connection owner or
  an explicitly delegated family member, plus Google writer/owner permission,
  checked each time. Ordinary family opt-in grants reading only. Change Google's sharing permission to enable/disable writing;
  no new key or relinking is needed. The model follows explicit user requests;
  this is not an independent approval dialog or OS sandbox.
- Create/edit supports title, location and start/end, including all-day dates.
  No guests, invitations, creation of recurring series or edits to entire series.
  A single ordinary occurrence without guests can be edited. Other existing
  fields are preserved. For invitations on personal accounts, user OAuth would
  be a better fit than the current service account.
- Creation uses a stable request ID to avoid duplicates after an uncertain
  network outcome, including across restarts. Updates use the event's exact
  ETag with `If-Match`, rejecting stale/concurrent modifications. After a failed
  write, Alina must verify the result before claiming success or trying again.
- Reads event title, dates, location, status and visibility for the requested
  interval. Recurrences are expanded by Google, with 50 events per page and a
  maximum 93-day interval. All-day end dates are exclusive. Descriptions,
  attendees and attachments are excluded from event listings and model output.
  Single-event inspection reads minimal attendee metadata internally to reject
  edits to meetings with guests. Google may hide private events.
- Calendar reads happen on request or through a user-requested scheduled task.
  Writes are limited to direct conversations.
  The tool is unavailable during dream and personal exploration. No polling,
  synchronization database or new background job is added.
- Requested details go to the configured AI provider and replies go through
  Telegram when that is the conversation channel. Tool results and replies
  remain in the normal archive and may be recalled or appear in reflection.
  A personal connection limits direct tool access, **not** visibility of existing
  conversation history within Alina's shared family memory. Disconnecting does
  not erase that history. Review provider data settings before connecting.
- Configured people share one trusted OS account. This is not an OS security
  boundary; another process or an unrestricted shell on that account can access
  its files. Family access to a binding ends when its owner or recipient leaves
  the configured family.

## One-time device setup

Create a Google Cloud project, enable the Google Calendar API and create a
service account without project IAM roles or domain-wide delegation. Install
its JSON key locally at:

```
$ALINA_HOME/integrations/google-calendar.json
```

`ALINA_HOME` defaults to `~/.config/alina`. Keep the directory private and the
key a regular file with mode `0600`. Never commit it. This setup is once per
installation; subsequent calendar connections happen through chat. Bindings
are stored separately in `integrations/calendars.json`. Access tokens are held
in memory and renewed when needed. Rotating the installed key takes effect on
the next calendar call.

No service-account login or mailbox is needed. Google sharing must be completed
by the calendar owner in Google's interface; a Telegram message cannot grant
that permission.
