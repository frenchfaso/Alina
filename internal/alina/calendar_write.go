package alina

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Pointer fields distinguish omitted values from intentional empty strings.
type calendarChange struct {
	Summary  *string       `json:"summary,omitempty"`
	Location *string       `json:"location,omitempty"`
	Start    *calendarTime `json:"start,omitempty"`
	End      *calendarTime `json:"end,omitempty"`
}

func calendarChangeSchema() map[string]any {
	str := map[string]any{"type": "string"}
	date := map[string]any{"type": "object", "properties": map[string]any{"date": str, "dateTime": str, "timeZone": str}, "additionalProperties": false}
	return map[string]any{"type": "object", "properties": map[string]any{"summary": str, "location": str, "start": date, "end": date}, "additionalProperties": false}
}
func calendarWritable(role string) bool { return role == "writer" || role == "owner" }
func calendarReadable(role string) bool { return role == "reader" || calendarWritable(role) }
func calendarHTTPStatus(err error, status int) bool {
	var e *remoteHTTPError
	return errors.As(err, &e) && e.Status == status
}

// Fetch extra safety metadata without exposing guests or private properties to the model.
type calendarEventDetail struct {
	calendarEvent
	Attendees          []struct{} `json:"attendees"`
	Recurrence         []string   `json:"recurrence"`
	EventType          string     `json:"eventType"`
	ExtendedProperties struct {
		Private map[string]string `json:"private"`
	} `json:"extendedProperties"`
}

const calendarEventFields = "id,etag,status,summary,start,end,location,transparency,visibility,attendees(self),recurrence,eventType,extendedProperties/private"

func (s *calendarState) eventGet(ctx context.Context, c calendarCredential, path string) (calendarEventDetail, error) {
	var result calendarEventDetail
	err := s.get(ctx, c, path, url.Values{"fields": {calendarEventFields}}, &result)
	return result, err
}
func calendarTimeValue(v calendarTime) (time.Time, error) {
	if v.Date != "" {
		if v.DateTime != "" || v.TimeZone != "" {
			return time.Time{}, errors.New("all-day dates cannot include dateTime or timeZone")
		}
		return time.Parse("2006-01-02", v.Date)
	}
	t, err := time.Parse(time.RFC3339, v.DateTime)
	if err != nil {
		return time.Time{}, errors.New("dateTime needs an RFC3339 timestamp with a time offset")
	}
	if v.TimeZone != "" {
		loc, err := time.LoadLocation(v.TimeZone)
		if err != nil {
			return time.Time{}, errors.New("invalid IANA timeZone")
		}
		_, offset := t.Zone()
		_, expected := t.In(loc).Zone()
		if offset != expected {
			return time.Time{}, errors.New("dateTime offset does not match timeZone on this date")
		}
	}
	return t, nil
}
func (v *calendarChange) validate(create bool) error {
	if v == nil {
		return errors.New("event changes are required")
	}
	if v.Summary == nil && v.Location == nil && v.Start == nil && v.End == nil {
		return errors.New("event changes are empty")
	}
	if v.Summary != nil && (strings.TrimSpace(*v.Summary) == "" || len(*v.Summary) > 1024) {
		return errors.New("summary must contain 1-1024 bytes")
	}
	if v.Location != nil && len(*v.Location) > 2048 {
		return errors.New("location exceeds 2048 bytes")
	}
	if create && (v.Summary == nil || v.Start == nil || v.End == nil) {
		return errors.New("create requires summary, start and end")
	}
	if (v.Start == nil) != (v.End == nil) {
		return errors.New("provide both start and end when changing event times")
	}
	if v.Start != nil {
		start, e1 := calendarTimeValue(*v.Start)
		end, e2 := calendarTimeValue(*v.End)
		if e1 != nil {
			return e1
		}
		if e2 != nil {
			return e2
		}
		if (v.Start.Date == "") != (v.End.Date == "") || !end.After(start) {
			return errors.New("start/end must use the same date format and end must be later (all-day end is exclusive)")
		}
	}
	return nil
}
func calendarEventIDValid(id string) bool {
	if len(id) == 0 || len(id) > 1024 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func calendarCreationID(b calendarBinding, u User, requestID string) string {
	// Stable across restarts/relinking. A retry must reuse the original request ID.
	return fmt.Sprintf("%x", sha256.Sum256([]byte(u.ID+"\x00"+b.CalendarID+"\x00"+requestID)))
}
func (s *calendarState) eventAction(ctx context.Context, c calendarCredential, b calendarBinding, u User, j *runningJob, a calendarArgs, canWrite bool) (string, error) {
	base := url.PathEscape(b.CalendarID) + "/events"
	if a.Action == "get" {
		if !calendarEventIDValid(a.EventID) {
			return "", errors.New("valid event_id required")
		}
		event, err := s.eventGet(ctx, c, base+"/"+url.PathEscape(a.EventID))
		return jsonText(event.calendarEvent), err
	}
	if !canWrite {
		return "", errors.New("family sharing permits reading only; the connection owner must explicitly delegate writing to you")
	}
	if j.Kind != "chat" {
		return "", errors.New("Calendar writes require an explicit user request in a direct conversation")
	}
	if err := a.Event.validate(a.Action == "create"); err != nil {
		return "", err
	}
	if a.Action == "create" && !safeID(a.RequestID) {
		return "", errors.New("create requires a stable request_id; reuse it unchanged for retries")
	}
	if a.Action == "update" && (!calendarEventIDValid(a.EventID) || a.ETag == "" || a.ETag == "*" || len(a.ETag) > 256 || strings.ContainsAny(a.ETag, "\r\n")) {
		return "", errors.New("update requires event_id and exact etag returned by get")
	}
	var probe struct {
		AccessRole string `json:"accessRole"`
	}
	if err := s.get(ctx, c, base, url.Values{"maxResults": {"1"}, "fields": {"accessRole"}}, &probe); err != nil {
		return "", err
	}
	if !calendarWritable(probe.AccessRole) {
		return "", errors.New("calendar is read-only; owner must grant event editing to the service account in Google Calendar")
	}
	if a.Action == "create" {
		return s.eventCreate(ctx, c, base, b, u, a)
	}
	path := base + "/" + url.PathEscape(a.EventID)
	old, err := s.eventGet(ctx, c, path)
	if err != nil {
		return "", err
	}
	if old.ETag != a.ETag {
		return "", errors.New("event changed since it was read; get current event and reconsider the requested change")
	}
	if old.Status == "cancelled" || len(old.Attendees) > 0 || len(old.Recurrence) > 0 || (old.EventType != "" && old.EventType != "default") {
		return "", errors.New("only active ordinary events without guests can be edited; select a single occurrence, not a recurring series")
	}
	var result calendarEvent
	err = s.request(ctx, c, http.MethodPatch, path, url.Values{"fields": {"id,etag,status,summary,start,end,location,transparency,visibility"}}, a.Event, map[string]string{"If-Match": a.ETag}, &result)
	if calendarHTTPStatus(err, 412) {
		return "", errors.New("event changed concurrently; get current event and reconsider the requested change")
	}
	if err != nil {
		return "", fmt.Errorf("update not confirmed; get event_id=%s to verify the current state before retrying: %w", a.EventID, err)
	}
	return jsonText(result), nil
}
func (s *calendarState) eventCreate(ctx context.Context, c calendarCredential, base string, b calendarBinding, u User, a calendarArgs) (string, error) {
	id := calendarCreationID(b, u, a.RequestID)
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(jsonText(a.Event))))
	path := base + "/" + id
	check := func(event calendarEventDetail) (string, error) {
		if event.Status == "cancelled" || event.ExtendedProperties.Private["alina_request"] != fingerprint {
			return "", errors.New("request_id already exists with different content or was deleted; do not create again unless the user requests a new event")
		}
		return jsonText(map[string]any{"event": event.calendarEvent, "already_created": true}), nil
	}
	old, err := s.eventGet(ctx, c, path)
	if err == nil {
		return check(old)
	}
	if !calendarHTTPStatus(err, 404) {
		return "", err
	}
	body := map[string]any{"id": id, "summary": *a.Event.Summary, "start": a.Event.Start, "end": a.Event.End, "extendedProperties": map[string]any{"private": map[string]string{"alina_request": fingerprint}}}
	if a.Event.Location != nil {
		body["location"] = *a.Event.Location
	}
	var result calendarEvent
	err = s.request(ctx, c, http.MethodPost, base, url.Values{"fields": {"id,etag,status,summary,start,end,location,transparency,visibility"}}, body, nil, &result)
	if calendarHTTPStatus(err, 409) {
		existing, readErr := s.eventGet(ctx, c, path)
		if readErr == nil {
			return check(existing)
		}
	}
	if err != nil {
		return "", fmt.Errorf("creation not confirmed; retry with the SAME request_id and event, or get event_id=%s; do not use a new request_id: %w", id, err)
	}
	return jsonText(map[string]any{"event": result, "already_created": false}), nil
}
