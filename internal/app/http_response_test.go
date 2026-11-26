package app_test

import (
	"fmt"
	"io"
	"net/http"
	"testing"
)

func TestUserTimelineFailureDoesNotAppendJSON(t *testing.T) {
	t.Parallel()
	// Given a saved conversation whose engine cannot open until discovery finishes.
	d, _, _, sessionID := startWithHeldDiscovery(t, false)

	// When the event reader fails after its SSE headers have been sent.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, fmt.Sprintf("%s/api/agents/default/sessions/%d/events", d.srv.URL, sessionID), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := d.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	// Then the stream ends for reconnect, without a JSON error in the SSE body.
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" || len(body) != 0 {
		t.Fatalf("failed timeline: HTTP %d, content type %q, body %q", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	// An error before the response starts still returns its normal JSON status.
	d.must(t, http.MethodGet, "/api/agents/default/sessions/999999/events", nil, nil, http.StatusNotFound)
	t.Log("artifact: failed SSE ended without JSON; unknown timeline still returned HTTP 404")
}
