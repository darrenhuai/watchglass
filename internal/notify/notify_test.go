package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendPostsToGenericWebhook(t *testing.T) {
	bodyCh := make(chan string, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodyCh <- string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// shoutrrr's generic service; "generic+http" targets a plain-HTTP webhook.
	url := "generic+" + ts.URL + "/hook"
	n, err := NewShoutrrr([]string{url})
	if err != nil {
		t.Fatalf("NewShoutrrr: %v", err)
	}
	if err := n.Send(context.Background(), "watchglass: printer-lcd", "pattern matched — PRINT COMPLETE"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case got := <-bodyCh:
		if !strings.Contains(got, "PRINT COMPLETE") {
			t.Errorf("webhook body = %q, want it to contain the message", got)
		}
	default:
		t.Fatal("webhook was never called")
	}
}

func TestNewShoutrrrRejectsBadURL(t *testing.T) {
	if _, err := NewShoutrrr([]string{"not-a-scheme"}); err == nil {
		t.Fatal("expected error for invalid notification URL")
	}
}
