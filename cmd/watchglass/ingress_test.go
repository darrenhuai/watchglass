package main

import (
	"strings"
	"testing"
)

// -ingress-from takes one IP address: the Supervisor's by default, a
// stand-in proxy's for testing. Anything else is refused at start-up.
func TestParseIngressFrom(t *testing.T) {
	for in, want := range map[string]string{
		supervisorAddr:       "172.30.32.2",
		"127.0.0.1":          "127.0.0.1",
		" ::1 ":              "::1",
		"::ffff:172.30.32.2": "::ffff:172.30.32.2",
	} {
		got, err := parseIngressFrom(in)
		if err != nil || got.String() != want {
			t.Errorf("parseIngressFrom(%q) = %v, %v; want %s", in, got, err, want)
		}
	}
	for _, in := range []string{"", "evil", "172.30.32.2:8080", "172.30.32.0/24", "http://172.30.32.2", "homeassistant.local"} {
		if _, err := parseIngressFrom(in); err == nil || !strings.Contains(err.Error(), "-ingress-from") || !strings.Contains(err.Error(), supervisorAddr) {
			t.Errorf("parseIngressFrom(%q) = %v, want an error naming the flag and the Supervisor's address", in, err)
		}
	}
}

// WATCHGLASS_INGRESS=1 is what addon/config.yaml sets; the flag and the
// env var are the same switch (envTrue's spellings).
func TestIngressEnvVar(t *testing.T) {
	for v, want := range map[string]bool{"1": true, "true": true, "": false, "0": false} {
		t.Setenv("WATCHGLASS_INGRESS", v)
		if got := envTrue("WATCHGLASS_INGRESS"); got != want {
			t.Errorf("WATCHGLASS_INGRESS=%q: ingress = %v, want %v", v, got, want)
		}
	}
}
