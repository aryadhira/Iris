// Package artifacts parses the [[artifact: <path>]] promise Hermes leaves at
// the end of a turn and strips the markers that were actually kept.
package artifacts

import (
	"regexp"
	"strings"
)

var markerLine = regexp.MustCompile(`(?m)^[ \t]*\[\[\s*artifact\s*:\s*(.+?)\s*\]\][ \t]*\r?\n?`)

// Marker is one promised file path, already normalised to forward slashes.
type Marker struct {
	Path string
}

// Found is a marker whose file exists and passed the room path guard.
type Found struct {
	Path string // relative room path
	Abs  string // absolute path
}

// Result separates clean prose from files that really exist and from promises
// that did not come true.
type Result struct {
	Body   string
	Files  []Found
	Broken []string
}

// Resolve is the guard/detection callback. It returns an absolute path only
// when the relative path is safe and the file exists.
type Resolve func(rel string) (abs string, err error)

// Parse returns every marker path in text, tolerating spaces and Windows
// slashes.
func Parse(text string) []Marker {
	matches := markerLine.FindAllStringSubmatch(text, -1)
	out := make([]Marker, 0, len(matches))
	for _, match := range matches {
		p := normalise(match[1])
		if p != "" {
			out = append(out, Marker{Path: p})
		}
	}
	return out
}

// Process strips markers whose files exist. A marker whose file is missing or
// unsafe stays in the body: a broken promise must remain visible.
func Process(text string, resolve Resolve) Result {
	matches := markerLine.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return Result{Body: text}
	}

	var body strings.Builder
	last := 0
	result := Result{}
	for _, match := range matches {
		body.WriteString(text[last:match[0]])
		raw := text[match[0]:match[1]]
		rel := normalise(text[match[2]:match[3]])
		abs, err := resolve(rel)
		if rel == "" || err != nil {
			result.Broken = append(result.Broken, rel)
			body.WriteString(raw)
		} else {
			result.Files = append(result.Files, Found{Path: rel, Abs: abs})
		}
		last = match[1]
	}
	body.WriteString(text[last:])

	result.Body = strings.TrimRight(body.String(), " \t\r\n")
	return result
}

func normalise(raw string) string {
	p := strings.TrimSpace(raw)
	p = strings.Trim(p, "\"'")
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "./")
	return strings.TrimSpace(p)
}
