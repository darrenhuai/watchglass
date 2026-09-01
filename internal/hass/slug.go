package hass

import "strings"

// Slug turns a free-form watch name into an MQTT/discovery-safe identifier:
// lowercase, with runs of anything outside [a-z0-9_-] collapsed to one '-'.
func Slug(name string) string {
	var b strings.Builder
	lastDash := true // suppress leading dashes
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
