package itest

import (
	"testing"

	"github.com/google/uuid"
)

type guidanceSet struct {
	KBVersion int64 `json:"kb_version"`
	Cards     []struct {
		Slug    string `json:"slug"`
		Version int    `json:"version"`
		Title   string `json:"title"`
	} `json:"cards"`
}

func (g guidanceSet) find(slug string) (int, string) {
	for _, c := range g.Cards {
		if c.Slug == slug {
			return c.Version, c.Title
		}
	}
	return 0, ""
}

// Guidance reaches citizens only after a second person approves it; corrections replace, never edit.
func TestGuidanceFourEyes(t *testing.T) {
	citizen := login(t)
	op := login(t, grant{Role: "OPERATOR", OrgCode: "command"})
	commander := login(t, grant{Role: "COMMANDER", OrgCode: "command"})
	commander2 := login(t, grant{Role: "COMMANDER", OrgCode: "command"})

	var before guidanceSet
	citizen.do("GET", "/guidance", nil, nil, &before, 200)
	if v, _ := before.find("gas-leak"); v == 0 || len(before.Cards) < 10 {
		t.Fatalf("seeded guidance missing: %d cards", len(before.Cards))
	}
	citizen.do("GET", "/guidance/admin", nil, nil, nil, 403)

	content := map[string]any{"title": "پس‌لرزه شبانه", "category": "after_quake", "keywords": []string{"شب", "پس لرزه"},
		"body": "در شب کفش و چراغ‌قوه کنار تخت بگذارید.", "actions": []string{"alerts"}}
	op.do("POST", "/guidance/cards", map[string]any{"slug": "Night Quake"}, nil, nil, 422)
	op.do("POST", "/guidance/cards", map[string]any{"slug": "night-aftershock", "title": "x", "category": "nope", "keywords": []string{"a"}, "body": "b"}, nil, nil, 422)
	body := map[string]any{"slug": "night-aftershock"}
	for k, v := range content {
		body[k] = v
	}
	var created struct {
		CardID    uuid.UUID `json:"card_id"`
		VersionID uuid.UUID `json:"version_id"`
	}
	op.do("POST", "/guidance/cards", body, nil, &created, 201)
	op.do("POST", "/guidance/cards", body, nil, nil, 409) // slug in use

	var mid guidanceSet
	citizen.do("GET", "/guidance", nil, nil, &mid, 200)
	if v, _ := mid.find("night-aftershock"); v != 0 {
		t.Fatal("pending guidance must not reach citizens")
	}
	op.do("POST", "/guidance/versions/"+created.VersionID.String()+"/approve", map[string]any{}, nil, nil, 403) // operators cannot publish
	op.do("POST", "/guidance/cards/"+created.CardID.String()+"/versions", content, nil, nil, 409)               // one pending at a time
	commander.do("POST", "/guidance/versions/"+created.VersionID.String()+"/approve", map[string]any{"reason": "بررسی شد"}, nil, nil, 200)

	var after guidanceSet
	citizen.do("GET", "/guidance", nil, nil, &after, 200)
	if v, _ := after.find("night-aftershock"); v != 1 || after.KBVersion <= before.KBVersion {
		t.Fatalf("published: v=%d kb %d -> %d", v, before.KBVersion, after.KBVersion)
	}

	// A correction written by a commander cannot be approved by that same commander.
	content["title"] = "پس‌لرزه در شب"
	var v2 struct {
		VersionID uuid.UUID `json:"version_id"`
	}
	commander.do("POST", "/guidance/cards/"+created.CardID.String()+"/versions", content, nil, &v2, 201)
	commander.do("POST", "/guidance/versions/"+v2.VersionID.String()+"/approve", map[string]any{}, nil, nil, 403)
	commander2.do("POST", "/guidance/versions/"+v2.VersionID.String()+"/reject", map[string]any{}, nil, nil, 422) // reason required
	commander2.do("POST", "/guidance/versions/"+v2.VersionID.String()+"/approve", map[string]any{}, nil, nil, 200)
	commander2.do("POST", "/guidance/versions/"+v2.VersionID.String()+"/approve", map[string]any{}, nil, nil, 409) // already published
	citizen.do("GET", "/guidance", nil, nil, &after, 200)
	if v, title := after.find("night-aftershock"); v != 2 || title != "پس‌لرزه در شب" {
		t.Fatalf("correction: v=%d %q", v, title)
	}

	// Retiring withdraws it from citizens and still moves the knowledge-base version forward.
	kb := after.KBVersion
	commander2.do("POST", "/guidance/cards/"+created.CardID.String()+"/retire", map[string]any{"reason": "ادغام با کارت دیگر"}, nil, nil, 200)
	citizen.do("GET", "/guidance", nil, nil, &after, 200)
	if v, _ := after.find("night-aftershock"); v != 0 || after.KBVersion <= kb {
		t.Fatalf("retired card still published or kb not advanced: v=%d kb %d -> %d", v, kb, after.KBVersion)
	}
}
