// Package reply is the one place that turns a raw Hermes reply into the text a
// human should see or hear.
//
// The rules are deliberately small and deterministic:
//
//   - a STATUS: RUNNING|DONE|BLOCKED|CONTINUE line is not part of the answer;
//   - [[artifact: ...]] markers are transport, not prose;
//   - a standalone -- becomes an em dash, while --- (used as a rule) disappears;
//   - everything inside a fenced block or inline `code` is byte-identical.
//
// Clean is idempotent. CleanKeepingArtifacts exists for the persistence path,
// where artifacts.Process has already decided which markers are real and which
// are broken promises; a broken promise must stay visible.
package reply

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	// The leading character class mirrors continuation.ParseStatus, plus a
	// heading marker, so "**STATUS: DONE**" and "> STATUS: RUNNING" are caught
	// as readily as a bare line.
	statusLine = regexp.MustCompile("(?im)^[ \\t>*_`#]*STATUS:[ \\t]*(?:RUNNING|DONE|BLOCKED|CONTINUE)\\b[^\\n]*(?:\\r?\\n)?(?:\\r?\\n)?")

	artifactLine = regexp.MustCompile(`(?m)^[ \t]*\[\[\s*artifact\s*:\s*(.+?)\s*\]\][ \t]*\r?\n?`)

	// A search snippet can be cut through a line. The full-line rule above
	// cannot see the prefix, so this second pass is intentionally local.
	residualStatus = regexp.MustCompile(`(?i)STATUS:\s*(?:RUNNING|DONE|BLOCKED|CONTINUE)\b[^\n]*`)
	taggedStatus   = regexp.MustCompile(`(?i)(?:<mark>)?STATUS(?:</mark>)?\s*:\s*(?:<mark>)?(?:RUNNING|DONE|BLOCKED|CONTINUE)\b[^\n]*`)
)

// Clean removes contract lines and markers, then turns a standalone -- into an
// em dash. Code is protected before any rule runs.
func Clean(text string) string {
	return clean(text, true, true)
}

// CleanKeepingArtifacts applies every visible-text rule except marker removal.
// The persistence path uses it after artifacts.Process has kept a missing
// artifact marker in the body on purpose.
func CleanKeepingArtifacts(text string) string {
	return clean(text, false, true)
}

// CleanSnippet is Clean for a database-highlighted search fragment: it also
// removes a STATUS line that the snippet window cut in half.
func CleanSnippet(text string) string {
	out := Clean(text)
	out = residualStatus.ReplaceAllString(out, "")
	out = taggedStatus.ReplaceAllString(out, "")
	return strings.TrimSpace(out)
}

func clean(text string, dropArtifacts, trim bool) string {
	if text == "" {
		return ""
	}
	masked, spans := maskCode(text)

	masked = statusLine.ReplaceAllString(masked, "")
	if dropArtifacts {
		masked = artifactLine.ReplaceAllString(masked, "")
	}
	masked = replaceStandaloneDashes(masked)
	masked = removeRuleLines(masked)
	if trim {
		masked = strings.TrimRight(masked, " \t\r\n")
	}

	for i, span := range spans {
		masked = strings.ReplaceAll(masked, sentinel(i), span)
	}
	return masked
}

// Stream is the streaming form of Clean. It never emits a line that could still
// become a STATUS/artifact/rule, and it holds a trailing run of '-' or a
// backtick until the next chunk decides what it is. The result is prefix-stable:
// text already sent is never rewritten.
type Stream struct {
	raw  string
	sent string
}

// Feed adds one raw delta and returns the newly visible text.
func (s *Stream) Feed(delta string) string {
	if delta == "" {
		return ""
	}
	s.raw += delta
	return s.emit(s.candidate())
}

// Flush releases the final, complete text at the end of a turn.
func (s *Stream) Flush() string {
	return s.emit(Clean(s.raw))
}

// Visible returns everything emitted so far (useful to a caller that keeps its
// own display buffer).
func (s *Stream) Visible() string { return s.sent }

func (s *Stream) emit(candidate string) string {
	if candidate == s.sent {
		return ""
	}
	if strings.HasPrefix(candidate, s.sent) {
		out := candidate[len(s.sent):]
		s.sent = candidate
		return out
	}
	// A shrinking candidate means an earlier heuristic missed something. Never
	// rewrite what the user already saw; the next complete line will resync.
	if strings.HasPrefix(s.sent, candidate) {
		return ""
	}
	return ""
}

func (s *Stream) candidate() string {
	raw := s.raw
	lastNewline := strings.LastIndexByte(raw, '\n')
	stableEnd := lastNewline + 1
	stable := raw[:stableEnd]
	tail := raw[stableEnd:]

	// Do not trim here: the newline is the boundary between this chunk and the
	// next one, and dropping it would glue two lines together on screen.
	prefix := clean(stable, true, false)
	if inFenceAtEnd(stable) {
		// The tail is code; its bytes are already final.
		return prefix + tail
	}
	if tail == "" {
		return prefix
	}

	left := strings.TrimLeft(tail, " \t")
	if isStatusCandidate(left) || isArtifactCandidate(left) || isRuleCandidate(left) {
		return prefix
	}
	// Inline code is safest held whole: a closing backtick can change whether
	// the -- inside it is prose or code.
	if strings.Contains(tail, "`") {
		return prefix
	}

	cut := len(tail)
	for cut > 0 && tail[cut-1] == '-' {
		cut--
	}
	return prefix + cleanFragment(tail[:cut])
}

func cleanFragment(text string) string {
	masked, spans := maskInline(text, 0)
	masked = replaceStandaloneDashes(masked)
	for i, span := range spans {
		masked = strings.ReplaceAll(masked, sentinel(i), span)
	}
	return masked
}

func sentinel(i int) string {
	return "\x00R" + strconv.Itoa(i) + "\x01"
}

func maskCode(text string) (string, []string) {
	var out strings.Builder
	spans := make([]string, 0, 4)
	for i := 0; i < len(text); {
		lineEnd := strings.IndexByte(text[i:], '\n')
		if lineEnd < 0 {
			lineEnd = len(text)
		} else {
			lineEnd = i + lineEnd + 1
		}
		line := text[i:lineEnd]

		if char, n, ok := openingFence(line); ok {
			end := findFenceEnd(text, i, lineEnd, char, n)
			out.WriteString(sentinel(len(spans)))
			spans = append(spans, text[i:end])
			i = end
			continue
		}

		maskedLine, lineSpans := maskInline(line, len(spans))
		out.WriteString(maskedLine)
		spans = append(spans, lineSpans...)
		i = lineEnd
	}
	return out.String(), spans
}

// maskInline protects every matched `code` span in one line. An unmatched
// backtick stays literal, which is what markdown renderers do too.
func maskInline(line string, base int) (string, []string) {
	var out strings.Builder
	spans := make([]string, 0, 2)
	for i := 0; i < len(line); {
		if line[i] != '`' {
			out.WriteByte(line[i])
			i++
			continue
		}
		run := backtickRun(line, i)
		closeAt := findBacktickRun(line, i+run, run)
		if closeAt < 0 {
			out.WriteString(line[i : i+run])
			i += run
			continue
		}
		end := closeAt + run
		out.WriteString(sentinel(base + len(spans)))
		spans = append(spans, line[i:end])
		i = end
	}
	return out.String(), spans
}

func backtickRun(s string, i int) int {
	n := 0
	for i+n < len(s) && s[i+n] == '`' {
		n++
	}
	return n
}

func findBacktickRun(s string, from, n int) int {
	for i := from; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		run := backtickRun(s, i)
		if run == n {
			return i
		}
		i += run
	}
	return -1
}

// openingFence recognises a line whose first non-space run is ``` or ~~~.
func openingFence(line string) (byte, int, bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) < 3 {
		return 0, 0, false
	}
	char := trimmed[0]
	if char != '`' && char != '~' {
		return 0, 0, false
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == char {
		n++
	}
	if n < 3 {
		return 0, 0, false
	}
	return char, n, true
}

func findFenceEnd(text string, start, next int, char byte, n int) int {
	for i := next; i < len(text); {
		lineEnd := strings.IndexByte(text[i:], '\n')
		if lineEnd < 0 {
			lineEnd = len(text)
		} else {
			lineEnd = i + lineEnd + 1
		}
		line := text[i:lineEnd]
		trimmed := strings.TrimLeft(line, " ")
		if len(line)-len(trimmed) <= 3 && len(trimmed) >= n {
			match := true
			for j := 0; j < n; j++ {
				if trimmed[j] != char {
					match = false
					break
				}
			}
			if match && strings.TrimSpace(trimmed[n:]) == "" {
				return lineEnd
			}
		}
		i = lineEnd
	}
	return len(text)
}

// inFenceAtEnd reports whether text ends inside a fenced code block.
func inFenceAtEnd(text string) bool {
	var (
		in    bool
		char  byte
		width int
	)
	for i := 0; i < len(text); {
		lineEnd := strings.IndexByte(text[i:], '\n')
		if lineEnd < 0 {
			lineEnd = len(text)
		} else {
			lineEnd = i + lineEnd + 1
		}
		line := text[i:lineEnd]
		if !in {
			if c, n, ok := openingFence(line); ok {
				in, char, width = true, c, n
			}
			i = lineEnd
			continue
		}
		trimmed := strings.TrimLeft(line, " ")
		if len(line)-len(trimmed) <= 3 && len(trimmed) >= width && trimmed[0] == char {
			match := true
			for j := 0; j < width; j++ {
				if trimmed[j] != char {
					match = false
					break
				}
			}
			if match && strings.TrimSpace(trimmed[width:]) == "" {
				in = false
			}
		}
		i = lineEnd
	}
	return in
}

func isStatusCandidate(s string) bool {
	s = strings.TrimLeft(s, " \t>*_`#")
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "status:") {
		return true
	}
	return strings.HasPrefix("status:", lower)
}

func isArtifactCandidate(s string) bool {
	s = strings.TrimLeft(s, " \t")
	return strings.HasPrefix(s, "[[")
}

func isRuleCandidate(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] != '-' {
			return false
		}
	}
	return true
}

func replaceStandaloneDashes(text string) string {
	if !strings.Contains(text, "--") {
		return text
	}
	var out strings.Builder
	out.Grow(len(text))
	for i := 0; i < len(text); {
		if text[i] == '-' && i+1 < len(text) && text[i+1] == '-' &&
			(i == 0 || isSpaceByte(text[i-1])) &&
			(i+2 == len(text) || isSpaceByte(text[i+2])) {
			start := i
			for start > 0 && text[start-1] == '-' {
				start--
			}
			end := i + 2
			for end < len(text) && text[end] == '-' {
				end++
			}
			if end-start == 2 {
				out.WriteString("—")
				i = end
				continue
			}
		}
		out.WriteByte(text[i])
		i++
	}
	return out.String()
}

func isSpaceByte(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r':
		return true
	default:
		return false
	}
}

func removeRuleLines(text string) string {
	lines := strings.SplitAfter(text, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if isRuleLine(lines[i]) {
			if i+1 < len(lines) && isBlankLine(lines[i+1]) {
				i++
			}
			continue
		}
		out = append(out, lines[i])
	}
	return strings.Join(out, "")
}

func isRuleLine(line string) bool {
	s := strings.TrimRight(line, "\r\n")
	s = strings.TrimSpace(s)
	if len(s) < 3 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] != '-' {
			return false
		}
	}
	return true
}

func isBlankLine(line string) bool {
	return strings.TrimSpace(line) == ""
}
