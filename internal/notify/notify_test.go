package notify

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
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

func TestNtfyURLPartitioning(t *testing.T) {
	n, err := NewShoutrrr([]string{
		"ntfy://ntfy.example/topic",
		"ntfys://ntfy.example/secure",
		"ntfy+http://127.0.0.1:8099/local",
		"generic+https://example.com/hook",
	})
	if err != nil {
		t.Fatalf("NewShoutrrr: %v", err)
	}
	want := []string{
		"https://ntfy.example/topic",
		"https://ntfy.example/secure",
		"http://127.0.0.1:8099/local",
	}
	if !reflect.DeepEqual(n.ntfyTargets, want) {
		t.Errorf("ntfy targets = %v, want %v", n.ntfyTargets, want)
	}
}

func TestSendImagePutsAttachmentToNtfy(t *testing.T) {
	var gotBody []byte
	var gotHeaders http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		gotBody, _ = io.ReadAll(r.Body)
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	n, err := NewShoutrrr([]string{"ntfy+" + ts.URL + "/mytopic"})
	if err != nil {
		t.Fatal(err)
	}
	png := []byte{0x89, 'P', 'N', 'G', 0}
	if err := n.SendImage(context.Background(), "watchglass: printer", "pattern matched", png); err != nil {
		t.Fatalf("SendImage: %v", err)
	}
	if !bytes.Equal(gotBody, png) {
		t.Errorf("body = %v, want png bytes", gotBody)
	}
	if gotHeaders.Get("X-Title") != "watchglass: printer" {
		t.Errorf("X-Title = %q", gotHeaders.Get("X-Title"))
	}
	if gotHeaders.Get("X-Message") != "pattern matched" {
		t.Errorf("X-Message = %q", gotHeaders.Get("X-Message"))
	}
	if gotHeaders.Get("X-Filename") != "watch.png" {
		t.Errorf("X-Filename = %q", gotHeaders.Get("X-Filename"))
	}
}

func TestSendImageWithoutNtfyFallsBackToText(t *testing.T) {
	bodyCh := make(chan string, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodyCh <- string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	n, err := NewShoutrrr([]string{"generic+" + ts.URL + "/hook"})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.SendImage(context.Background(), "t", "image-less body", []byte{1, 2, 3}); err != nil {
		t.Fatalf("SendImage fallback: %v", err)
	}
	select {
	case got := <-bodyCh:
		if !strings.Contains(got, "image-less body") {
			t.Errorf("fallback text not delivered: %q", got)
		}
	default:
		t.Fatal("generic webhook never called")
	}
}

func TestSendStillReachesNtfyAsText(t *testing.T) {
	var gotHeaders http.Header
	var gotBody []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	n, err := NewShoutrrr([]string{"ntfy+" + ts.URL + "/mytopic"})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Send(context.Background(), "title", "plain text"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if string(gotBody) != "plain text" {
		t.Errorf("text body = %q", gotBody)
	}
	if gotHeaders.Get("X-Title") != "title" {
		t.Errorf("X-Title = %q", gotHeaders.Get("X-Title"))
	}
}
