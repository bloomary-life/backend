package main

import (
	"strings"
	"testing"
)

// Verifies: BE-007
func TestLogResponseDataRedactsUserContent(t *testing.T) {
	data := `{"drafts":[{"draftId":"d1","body":"my private post","image":{"downloadUrl":"https://r2.test/x?X-Amz-Signature=abc"}}],"profile":{"name":"Jane Doe","picture":"https://media.test/p.jpg"},"content":[{"text":"draft text"}]}`
	logged := logResponseData(data, "application/json")
	for _, leaked := range []string{"my private post", "Jane Doe", "media.test", "draft text", "abc"} {
		if strings.Contains(logged, leaked) {
			t.Fatalf("logged %q in %s", leaked, logged)
		}
	}
	if !strings.Contains(logged, `"draftId":"d1"`) {
		t.Fatalf("expected non-personal fields to stay, got %s", logged)
	}
}

// Verifies: BE-007
func TestLogResponseDataRedactsNonJSONBody(t *testing.T) {
	if logged := logResponseData("PK\x03\x04 draft export", "application/zip"); logged != redactedLogValue {
		t.Fatalf("expected non-JSON body to be redacted, got %q", logged)
	}
}
