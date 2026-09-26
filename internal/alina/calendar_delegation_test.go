package alina

import (
	"strings"
	"testing"
)

func TestCalendarDelegationLifecycle(t *testing.T) {
	e, j, b, f := calendarWriteEngine(t)
	// A named delegate gets reading and writing even without broad family sharing.
	b = calendarConnect(t, e, j, false)
	grant := calendarArgs{Action: "grant_write", ID: b.ID, UserID: "bob", Consent: true}
	calendarCall(t, e, j, grant)
	calendarCall(t, e, j, grant) // idempotent grant
	// Re-linking must not silently remove separately granted permissions.
	b = calendarConnect(t, e, j, false)
	if len(b.Writers) != 1 {
		t.Fatal("grant lost or duplicated on relink")
	}
	j.Owner = "local:bob"
	if !strings.Contains(calendarCall(t, e, j, calendarArgs{Action: "list"}), b.ID) {
		t.Fatal("delegate cannot see binding")
	}
	create := calendarCreateArgs(b)
	calendarCall(t, e, j, create)
	id := calendarCreationID(b, e.Config.Users[1], create.RequestID)
	title := "Changed by delegate"
	calendarCall(t, e, j, calendarArgs{Action: "update", ID: b.ID, EventID: id, ETag: `"v1"`, Event: &calendarChange{Summary: &title}})
	if f.posts != 1 || f.patches != 1 {
		t.Fatal("delegated writes missing")
	}
	for _, a := range []calendarArgs{grant, {Action: "revoke_write", ID: b.ID, UserID: "bob"}, {Action: "disconnect", ID: b.ID}} {
		if _, err := e.calendarTool(j, jsonText(a)); err == nil {
			t.Fatal("delegate managed owner's binding")
		}
	}
	j.Owner = "local:alice"
	revoke := calendarArgs{Action: "revoke_write", ID: b.ID, UserID: "bob"}
	calendarCall(t, e, j, revoke)
	calendarCall(t, e, j, revoke)
	j.Owner = "local:bob"
	if _, err := e.calendarTool(j, jsonText(create)); err == nil {
		t.Fatal("revoked delegate could write")
	}
	if got := calendarCall(t, e, j, calendarArgs{Action: "list"}); got != "[]" {
		t.Fatal("private binding still visible", got)
	}
}
func TestCalendarDelegationChecksConsentFamilyAndGoogle(t *testing.T) {
	e, j, b, f := calendarWriteEngine(t)
	for _, a := range []calendarArgs{
		{Action: "grant_write", ID: b.ID, UserID: "bob"},
		{Action: "grant_write", ID: b.ID, UserID: "eve", Consent: true},
		{Action: "grant_write", ID: b.ID, UserID: "alice", Consent: true},
		{Action: "grant_write", ID: b.ID, UserID: "unknown", Consent: true},
	} {
		if _, err := e.calendarTool(j, jsonText(a)); err == nil {
			t.Fatal("invalid grant accepted")
		}
	}
	grant := calendarArgs{Action: "grant_write", ID: b.ID, UserID: "bob", Consent: true}
	j.Kind = "task"
	if _, err := e.calendarTool(j, jsonText(grant)); err == nil {
		t.Fatal("background grant accepted")
	}
	j.Kind = "chat"
	members := calendarCall(t, e, j, calendarArgs{Action: "members"})
	if !strings.Contains(members, "bob") || strings.Contains(members, "eve") || strings.Contains(members, "telegram_id") {
		t.Fatal("members not scoped", members)
	}
	calendarCall(t, e, j, grant)
	j.Owner = "local:bob"
	f.role = "reader"
	if _, err := e.calendarTool(j, jsonText(calendarCreateArgs(b))); err == nil || f.posts != 0 {
		t.Fatal("delegation bypassed Google")
	}
	f.role = "writer"
	e.Config.Users[1].Family = "other"
	if _, err := e.calendarTool(j, jsonText(calendarCreateArgs(b))); err == nil {
		t.Fatal("departed member could write")
	}
	e.Config.Users[0].Family = "other"
	if _, err := e.calendarTool(j, jsonText(calendarCreateArgs(b))); err == nil {
		t.Fatal("grant transferred to another family")
	}
	// Revocation works even after the recipient leaves the family.
	j.Owner = "local:alice"
	calendarCall(t, e, j, calendarArgs{Action: "revoke_write", ID: b.ID, UserID: "bob"})
}
