package alina

import "errors"

// Anchor each grant to its original family; moving people to another family
// cannot transfer the grant with them.
type calendarDelegate struct {
	UserID string `json:"user_id"`
	Family string `json:"family"`
}

func calendarCanWrite(b calendarBinding, u User, users []User) bool {
	if b.UserID == u.ID {
		return true
	}
	if u.Family == "" {
		return false
	}
	ownerFamily := ""
	for _, p := range users {
		if p.ID == b.UserID {
			ownerFamily = p.Family
			break
		}
	}
	if ownerFamily != u.Family {
		return false
	}
	for _, d := range b.Writers {
		if d.UserID == u.ID && d.Family == u.Family {
			return true
		}
	}
	return false
}
func (e *Engine) calendarDelegation(u User, a calendarArgs, path string, bindings []calendarBinding) (string, error) {
	for i := range bindings {
		b := &bindings[i]
		if b.ID != a.ID || b.UserID != u.ID {
			continue
		}
		if a.UserID == "" || a.UserID == u.ID {
			return "", errors.New("specify another family member's user_id; use members")
		}
		if a.Action == "grant_write" {
			if !a.Consent {
				return "", errors.New("grant_write requires the owner's explicit consent for this person and calendar")
			}
			found := false
			for _, p := range e.Config.Users {
				if p.ID == a.UserID && p.Family != "" && p.Family == u.Family {
					found = true
				}
			}
			if !found {
				return "", errors.New("recipient must currently belong to the owner's family; use members")
			}
		}
		// Replace a repeated grant rather than accumulating duplicate entries.
		kept := make([]calendarDelegate, 0, len(b.Writers)+1)
		for _, d := range b.Writers {
			if d.UserID != a.UserID {
				kept = append(kept, d)
			}
		}
		if a.Action == "grant_write" {
			kept = append(kept, calendarDelegate{UserID: a.UserID, Family: u.Family})
		}
		b.Writers = kept
		if err := writeJSON(path, bindings); err != nil {
			return "", err
		}
		return jsonText(map[string]any{"calendar": b, "delegation": a.Action, "user_id": a.UserID, "note": "Google must still grant writer/owner access. Revocation removes delegated writing; ordinary family reading and historical conversations are unchanged."}), nil
	}
	return "", errors.New("owned calendar binding not found")
}
