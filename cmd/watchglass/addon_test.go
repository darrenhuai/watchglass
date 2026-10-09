package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The add-on's files live in addon/, two levels up from this package.
func addonFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// dockerInstructions returns a Dockerfile's instructions, one string per
// instruction with continuation lines joined and comments dropped.
func dockerInstructions(src string) []string {
	var out []string
	cur := ""
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if cur == "" && (trimmed == "" || strings.HasPrefix(trimmed, "#")) {
			continue
		}
		if strings.HasSuffix(trimmed, "\\") {
			cur += strings.TrimSuffix(trimmed, "\\") + " "
			continue
		}
		out = append(out, cur+trimmed)
		cur = ""
	}
	return out
}

// lastInstruction returns the arguments of the last instruction named
// kw, or "" if there is none.
func lastInstruction(ins []string, kw string) string {
	found := ""
	for _, in := range ins {
		if k, rest, ok := strings.Cut(in, " "); ok && strings.EqualFold(k, kw) {
			found = strings.TrimSpace(rest)
		}
	}
	return found
}

// On a real Supervisor (2026.09.3) the add-on's config folder is created
// root:root 0755 and mounted at /config, and the container runs as the
// image's USER. The published image runs as the watchglass user, which
// can't create config.yaml there, so no add-on release up to v0.1.10
// ever started. The add-on therefore builds its own image from the
// published one: started as root, it hands /config to the watchglass user
// and drops to it (run.sh). This pins the pieces that make that work.
func TestAddonBuildsItsOwnImage(t *testing.T) {
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(addonFile(t, "addon/config.yaml")), &cfg); err != nil {
		t.Fatal(err)
	}
	if img, ok := cfg["image"]; ok {
		t.Errorf("addon/config.yaml sets image: %v; with it the Supervisor runs the published image as the watchglass user, which can't write the root-owned /config, instead of building addon/Dockerfile", img)
	}
	if v, _ := cfg["version"].(string); v == "" {
		t.Error("addon/config.yaml has no version: the Supervisor passes it to the build as BUILD_VERSION, the published tag the add-on starts from")
	}

	ins := dockerInstructions(addonFile(t, "addon/Dockerfile"))
	if from := lastInstruction(ins, "FROM"); from != "ghcr.io/darrenhuai/watchglass:${BUILD_VERSION}" {
		t.Errorf("addon/Dockerfile FROM %q, want the published image at the add-on's version, ghcr.io/darrenhuai/watchglass:${BUILD_VERSION}", from)
	}
	// A variable in FROM only expands if an ARG declares it before the
	// FROM. Without one, ${BUILD_VERSION} is empty there, the image
	// reference is invalid and the Supervisor's build fails.
	declared := false
	for _, in := range ins {
		k, rest, _ := strings.Cut(in, " ")
		if strings.EqualFold(k, "FROM") {
			break
		}
		name, _, _ := strings.Cut(strings.TrimSpace(rest), "=")
		if strings.EqualFold(k, "ARG") && name == "BUILD_VERSION" {
			declared = true
		}
	}
	if !declared {
		t.Error("addon/Dockerfile has no ARG BUILD_VERSION before its FROM, so the FROM's ${BUILD_VERSION} is empty and the build fails")
	}
	if user := lastInstruction(ins, "USER"); user != "root" {
		t.Errorf("addon/Dockerfile USER %q, want root: run.sh needs it to hand /config over before dropping to the watchglass user", user)
	}
	var entry []string
	if err := json.Unmarshal([]byte(lastInstruction(ins, "ENTRYPOINT")), &entry); err != nil || len(entry) == 0 || !strings.HasSuffix(entry[len(entry)-1], "watchglass-addon") {
		t.Errorf("addon/Dockerfile ENTRYPOINT %v (%v), want run.sh (copied to /usr/local/bin/watchglass-addon)", entry, err)
	}
	if len(entry) > 0 {
		if cp := lastInstruction(ins, "COPY"); !strings.HasPrefix(cp, "run.sh ") || !strings.HasSuffix(cp, " "+entry[len(entry)-1]) {
			t.Errorf("addon/Dockerfile COPY %q, want run.sh copied to the ENTRYPOINT's path", cp)
		}
	}

	// An ENTRYPOINT resets the CMD a child image inherits, so the add-on
	// repeats it; it must stay the plain image's CMD.
	var addonCmd, plainCmd []string
	if err := json.Unmarshal([]byte(lastInstruction(ins, "CMD")), &addonCmd); err != nil {
		t.Fatalf("addon/Dockerfile CMD: %v", err)
	}
	if err := json.Unmarshal([]byte(lastInstruction(dockerInstructions(addonFile(t, "Dockerfile")), "CMD")), &plainCmd); err != nil {
		t.Fatalf("Dockerfile CMD: %v", err)
	}
	if !reflect.DeepEqual(addonCmd, plainCmd) {
		t.Errorf("addon/Dockerfile CMD %q differs from the plain image's %q", addonCmd, plainCmd)
	}
}

// run.sh, run with fake id, chown, setpriv and watchglass: as root it
// gives /config to the watchglass user and execs watchglass through
// setpriv as that user with the image's arguments; as anyone else it
// touches nothing and runs watchglass directly. If the chown fails it
// stops there with an error, rather than starting watchglass on a folder
// it can't write. The script runs from a copy whose
// /usr/local/bin/watchglass points at the fake, so a real install on the
// test machine is never started.
func TestAddonRunScript(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh to run addon/run.sh with")
	}
	const real = "/usr/local/bin/watchglass"
	src := addonFile(t, "addon/run.sh")
	if n := strings.Count(src, real+" "); n != 2 {
		t.Fatalf("addon/run.sh names %s %d times, want 2 (the root and the non-root exec)", real, n)
	}
	cases := []struct {
		name, uid  string
		chownFails bool
	}{
		{"uid 0", "0", false},
		{"uid 1000", "1000", false},
		{"uid 0 chown fails", "0", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bin := t.TempDir()
			fakeWG := filepath.ToSlash(filepath.Join(bin, "watchglass"))
			logf := filepath.Join(bin, "calls.log")
			fake := func(name, body string) {
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			fake("id", `echo `+c.uid)
			chown := `echo "chown $*" >> "$CALLS"`
			if c.chownFails {
				chown += "\nexit 1"
			}
			fake("chown", chown)
			fake("setpriv", `echo "setpriv $*" >> "$CALLS"`)
			fake("watchglass", `echo "watchglass $*" >> "$CALLS"`)
			script := filepath.Join(bin, "run.sh")
			if err := os.WriteFile(script, []byte(strings.ReplaceAll(src, real+" ", fakeWG+" ")), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(sh, script, "-config", "/config/config.yaml", "-listen", "0.0.0.0:8080")
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "CALLS="+logf)
			out, runErr := cmd.CombinedOutput()
			calls, _ := os.ReadFile(logf)
			got := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(calls), "\r\n", "\n")), "\n")
			args := "-config /config/config.yaml -listen 0.0.0.0:8080"
			want := []string{"watchglass " + args}
			switch {
			case c.chownFails:
				want = []string{"chown -hR watchglass:watchglass /config"}
			case c.uid == "0":
				want = []string{
					"chown -hR watchglass:watchglass /config",
					"setpriv --reuid=watchglass --regid=watchglass --init-groups --no-new-privs -- " + fakeWG + " " + args,
				}
			}
			if (runErr != nil) != c.chownFails || !reflect.DeepEqual(got, want) {
				t.Errorf("%s: calls %q (err %v, output %s), want %q with an error %v", c.name, got, runErr, out, want, c.chownFails)
			}
		})
	}
}

// The Supervisor builds the add-on on Linux, where a carriage return at
// the end of a line in run.sh is part of the command: "set -eu\r" fails
// and the add-on never starts. addonFile folds CRLF for the other tests,
// so this one reads run.sh as it is on disk. A Windows checkout with
// core.autocrlf=true fails here, and an image built by hand from it would
// fail the same way.
func TestAddonRunScriptUsesLF(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "addon", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "\r"); n > 0 {
		t.Errorf("addon/run.sh has %d carriage returns; sh in the add-on's image can't run it", n)
	}
}
