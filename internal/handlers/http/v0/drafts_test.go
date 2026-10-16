package v0

import (
	"encoding/json"
	"testing"
)

func TestDraftUpsertsMapsRemoveImage(t *testing.T) {
	var req draftSyncRequest
	body := `{"upserts":[{"draftId":"draft-1","body":"body","updatedAt":"2026-10-08T10:00:00Z","removeImage":true}]}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}

	upserts, err := draftUpserts(req.Upserts)
	if err != nil {
		t.Fatal(err)
	}
	if len(upserts) != 1 || !upserts[0].RemoveImage || upserts[0].Image != nil {
		t.Fatalf("expected removeImage upsert without image, got %#v", upserts)
	}
}
