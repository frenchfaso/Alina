package alina

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type calendarWriteFixture struct {
	role                   string
	posts, patches         int
	events                 map[string]map[string]any
	loseResponse, conflict bool
}

func (f *calendarWriteFixture) roundTrip(r *http.Request) (*http.Response, error) {
	reply := func(code int, v any) (*http.Response, error) {
		return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(jsonText(v)))}, nil
	}
	if r.URL.Scheme != "https" || r.URL.Host != "www.googleapis.com" {
		return nil, errors.New("wrong endpoint")
	}
	if r.URL.Query().Get("fields") == "accessRole" {
		return reply(200, map[string]string{"accessRole": f.role})
	}
	parts := strings.Split(r.URL.Path, "/")
	id := parts[len(parts)-1]
	switch r.Method {
	case "GET":
		if v, ok := f.events[id]; ok {
			return reply(200, v)
		}
		return reply(404, map[string]any{})
	case "POST":
		f.posts++
		var v map[string]any
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			return nil, err
		}
		id = v["id"].(string)
		if _, ok := f.events[id]; ok {
			return reply(409, map[string]any{})
		}
		v["etag"] = `"v1"`
		v["status"] = "confirmed"
		v["eventType"] = "default"
		f.events[id] = v
		if f.loseResponse {
			return nil, errors.New("response lost after server saved event")
		}
		return reply(200, v)
	case "PATCH":
		f.patches++
		v := f.events[id]
		if f.conflict || r.Header.Get("If-Match") != v["etag"] {
			return reply(412, map[string]any{})
		}
		var changes map[string]any
		if err := json.NewDecoder(r.Body).Decode(&changes); err != nil {
			return nil, err
		}
		for key, value := range changes {
			v[key] = value
		}
		v["etag"] = `"v2"`
		return reply(200, v)
	}
	return nil, errors.New("unexpected method")
}
func calendarWriteEngine(t *testing.T) (*Engine, *runningJob, calendarBinding, *calendarWriteFixture) {
	e, j, _ := calendarFixture(t)
	b := calendarConnect(t, e, j, true)
	f := &calendarWriteFixture{role: "writer", events: map[string]map[string]any{}}
	e.calendar.client.Transport = calendarTransport(f.roundTrip)
	return e, j, b, f
}
func calendarCreateArgs(b calendarBinding) calendarArgs {
	title := "Dentist"
	return calendarArgs{Action: "create", ID: b.ID, RequestID: "dentist-request-1", Event: &calendarChange{Summary: &title, Start: &calendarTime{DateTime: "2026-10-01T10:00:00+02:00", TimeZone: "Europe/Rome"}, End: &calendarTime{DateTime: "2026-10-01T11:00:00+02:00", TimeZone: "Europe/Rome"}}}
}
func TestCalendarWritesRequireOwnerChatAndGooglePermission(t *testing.T) {
	e, j, b, f := calendarWriteEngine(t)
	a := calendarCreateArgs(b)
	for _, role := range []string{"reader", "freeBusyReader", "none", ""} {
		f.role = role
		if _, err := e.calendarTool(j, jsonText(a)); err == nil {
			t.Fatal("write accepted", role)
		}
	}
	f.role = "writer"
	j.Owner = "local:bob"
	if _, err := e.calendarTool(j, jsonText(a)); err == nil {
		t.Fatal("family write accepted")
	}
	j.Owner = "local:alice"
	for _, kind := range []string{"task", "dream", "initiative"} {
		j.Kind = kind
		if _, err := e.calendarTool(j, jsonText(a)); err == nil {
			t.Fatal("background write accepted")
		}
	}
	if f.posts != 0 || f.patches != 0 {
		t.Fatal("denied requests mutated Google")
	}
	j.Kind = "chat"
	calendarCall(t, e, j, a)
	if f.posts != 1 {
		t.Fatal("owner cannot create")
	}
	// Writer calendars can be linked with the original consent flow.
	calendarConnect(t, e, j, true)
}
func TestCalendarCreateRetryAfterLostResponse(t *testing.T) {
	e, j, b, f := calendarWriteEngine(t)
	a := calendarCreateArgs(b)
	f.loseResponse = true
	out, err := e.calendarTool(j, jsonText(a))
	if err == nil || out != "" || !strings.Contains(err.Error(), "SAME request_id") {
		t.Fatal("ambiguous success not reported", out, err)
	}
	out = calendarCall(t, e, j, a)
	if !strings.Contains(out, `"already_created":true`) || f.posts != 1 || len(f.events) != 1 {
		t.Fatal("retry duplicated creation", out)
	}
	changed := "Changed request"
	a.Event.Summary = &changed
	if _, err = e.calendarTool(j, jsonText(a)); err == nil || f.posts != 1 {
		t.Fatal("reused request with new content")
	}
	// Stable ID even if the local binding is re-created.
	id := calendarCreationID(b, e.Config.Users[0], a.RequestID)
	b.ID = "replacement"
	if calendarCreationID(b, e.Config.Users[0], a.RequestID) != id {
		t.Fatal("relink breaks idempotency")
	}
}
func TestCalendarUpdatePreservesFieldsAndDetectsConflicts(t *testing.T) {
	e, j, b, f := calendarWriteEngine(t)
	a := calendarCreateArgs(b)
	calendarCall(t, e, j, a)
	id := calendarCreationID(b, e.Config.Users[0], a.RequestID)
	f.events[id]["description"] = "untouched private description"
	old := calendarCall(t, e, j, calendarArgs{Action: "get", ID: b.ID, EventID: id})
	if strings.Contains(old, "private description") || strings.Contains(old, "alina_request") {
		t.Fatal("get exposed extra fields")
	}
	location := "New location"
	update := calendarArgs{Action: "update", ID: b.ID, EventID: id, ETag: `"v1"`, Event: &calendarChange{Location: &location}}
	calendarCall(t, e, j, update)
	if f.events[id]["description"] != "untouched private description" || f.events[id]["summary"] != "Dentist" || f.events[id]["location"] != location {
		t.Fatal("patch lost unchanged fields")
	}
	if _, err := e.calendarTool(j, jsonText(update)); err == nil || f.patches != 1 {
		t.Fatal("stale etag accepted")
	}
	update.ETag = `"v2"`
	f.conflict = true
	if _, err := e.calendarTool(j, jsonText(update)); err == nil || !strings.Contains(err.Error(), "concurrently") {
		t.Fatal("race not detected", err)
	}
}
func TestCalendarRefusesGuestsSeriesAndCancelledEvents(t *testing.T) {
	for _, field := range []string{"attendees", "recurrence", "status", "eventType"} {
		t.Run(field, func(t *testing.T) {
			e, j, b, f := calendarWriteEngine(t)
			a := calendarCreateArgs(b)
			calendarCall(t, e, j, a)
			id := calendarCreationID(b, e.Config.Users[0], a.RequestID)
			switch field {
			case "attendees":
				f.events[id][field] = []any{map[string]string{"email": "guest@example.com"}}
			case "recurrence":
				f.events[id][field] = []string{"RRULE:FREQ=WEEKLY"}
			case "status":
				f.events[id][field] = "cancelled"
			case "eventType":
				f.events[id][field] = "outOfOffice"
			}
			title := "Changed"
			a = calendarArgs{Action: "update", ID: b.ID, EventID: id, ETag: `"v1"`, Event: &calendarChange{Summary: &title}}
			if _, err := e.calendarTool(j, jsonText(a)); err == nil || f.patches != 0 {
				t.Fatal("unsafe event update", field)
			}
		})
	}
}
func TestCalendarWriteValidation(t *testing.T) {
	e, j, b, f := calendarWriteEngine(t)
	for _, mutate := range []func(*calendarArgs){
		func(a *calendarArgs) { a.RequestID = "" },
		func(a *calendarArgs) { a.Event = nil },
		func(a *calendarArgs) { a.Event.Start.DateTime = "2026-10-01T10:00:00" },
		func(a *calendarArgs) { a.Event.Start.TimeZone = "America/New_York" },
		func(a *calendarArgs) { a.Event.End = nil },
		func(a *calendarArgs) { a.Event.End.DateTime = "2026-09-01T10:00:00+02:00" },
		func(a *calendarArgs) { a.Event.End = &calendarTime{Date: "2026-10-02"} },
	} {
		a := calendarCreateArgs(b)
		mutate(&a)
		if _, err := e.calendarTool(j, jsonText(a)); err == nil {
			t.Fatal("invalid create accepted")
		}
	}
	if _, err := e.calendarTool(j, `{"action":"create","event":{"attendees":[]}}`); err == nil {
		t.Fatal("unknown event fields accepted")
	}
	if f.posts != 0 {
		t.Fatal("invalid input reached write endpoint")
	}
	a := calendarCreateArgs(b)
	a.Event.Start = &calendarTime{Date: "2026-10-01"}
	a.Event.End = &calendarTime{Date: "2026-10-02"}
	calendarCall(t, e, j, a)
}
