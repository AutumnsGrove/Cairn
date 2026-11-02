package banks

import "strings"

// Slug turns a chip label into the stable key stored with an answer: lower case, ASCII letters and digits kept,
// every run of anything else collapsed to one underscore.
//
// It is deliberately lossy. A value is a key, not a display string: the label beside it can be reworded and the
// answers already recorded still compare, filter and count (PLAN.md 2, "question sets"). A label with nothing
// usable left, such as "!!!" or a script outside Latin, has no key and is a load error rather than a silent
// empty one.
func Slug(label string) (string, bool) {
	var b strings.Builder
	b.Grow(len(label))
	lastUnderscore := true // trims a leading underscore
	for _, r := range strings.ToLower(label) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	out := strings.TrimSuffix(b.String(), "_")
	if out == "" {
		return "", false
	}
	return out, true
}

// IsSlug reports whether s is already in the stored form a value must use. A hand-written value has to be, so a
// bank cannot store "Cozy" for one chip and "cozy" for another.
func IsSlug(s string) bool {
	if s == "" {
		return false
	}
	if s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			continue
		}
		return false
	}
	return true
}
