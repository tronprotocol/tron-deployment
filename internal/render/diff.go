package render

import "strings"

// DiffLines compares positional lines, redacts witness material, and emits optional equal context lines.
func DiffLines(old, new []string, contextLines int) []string {
	return diffLines(old, new, contextLines, false)
}

// DiffText trims trailing newlines, splits live and desired text, then applies
// redacted, including witness material in multi-line arrays.
func DiffText(live, desired string, contextLines int) []string {
	old := strings.Split(strings.TrimRight(live, "\n"), "\n")
	new := strings.Split(strings.TrimRight(desired, "\n"), "\n")
	return diffLines(old, new, contextLines, true)
}

func diffLines(old, new []string, contextLines int, emitEmpty bool) []string {
	oldR, newR := RedactWitnessLines(old), RedactWitnessLines(new)
	max := len(old)
	if len(new) > max {
		max = len(new)
	}
	var out []string
	for i := 0; i < max; i++ {
		var a, b string
		if i < len(old) {
			a = old[i]
		}
		if i < len(new) {
			b = new[i]
		}
		if a == b {
			continue
		}
		if contextLines > 0 {
			lo := i - contextLines
			if lo < 0 {
				lo = 0
			}
			for j := lo; j < i; j++ {
				if j < len(old) {
					out = append(out, "  "+oldR[j])
				}
			}
		}
		if i < len(old) && (emitEmpty || a != "") {
			out = append(out, "- "+oldR[i])
		}
		if i < len(new) && (emitEmpty || b != "") {
			out = append(out, "+ "+newR[i])
		}
	}
	return out
}
