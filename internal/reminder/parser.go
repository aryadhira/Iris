// Package reminder parses Boss' own Indonesian reminder sentence before it is
// dispatched. It is deliberately conservative: no explicit time, no reminder.
package reminder

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultLocation is the wall clock Boss actually lives on.
const DefaultLocation = "Asia/Jakarta"

var (
	triggerRE  = regexp.MustCompile(`(?i)\b(?:ingatkan|ingetin)\b`)
	relativeRE = regexp.MustCompile(`(?i)\bdalam\s+(\d{1,3})\s*(detik|menit|jam)\b`)
	absoluteRE = regexp.MustCompile(`(?i)\b(besok|nanti)?\s*(?:jam|pukul)\s*(\d{1,2})(?:[:.](\d{2}))?\s*(pagi|siang|sore|malam)?\b`)
)

// Parsed is one confident reminder. DueAt is already in Loc.
type Parsed struct {
	Text  string
	DueAt time.Time
}

// Jakarta returns the Iris timezone, with a fixed +07:00 fallback for hosts
// whose tzdata is missing. The fallback is the same offset, never a guess.
func Jakarta() *time.Location {
	loc, err := time.LoadLocation(DefaultLocation)
	if err != nil {
		return time.FixedZone("WIB", 7*60*60)
	}
	return loc
}

// Parse reads one typed message. It returns false whenever the sentence has no
// trigger word or no explicit, valid time. A wrong reminder is worse than none.
func Parse(text string, now time.Time, loc *time.Location) (Parsed, bool) {
	if loc == nil {
		loc = Jakarta()
	}
	if !triggerRE.MatchString(text) {
		return Parsed{}, false
	}
	localNow := now.In(loc)

	trigger := triggerRE.FindString(text)
	if match := relativeRE.FindStringSubmatch(text); match != nil {
		n, err := strconv.Atoi(match[1])
		if err != nil || n <= 0 {
			return Parsed{}, false
		}
		var duration time.Duration
		switch strings.ToLower(match[2]) {
		case "detik":
			duration = time.Duration(n) * time.Second
		case "menit":
			duration = time.Duration(n) * time.Minute
		case "jam":
			duration = time.Duration(n) * time.Hour
		}
		due := localNow.Add(duration)
		return Parsed{Text: cleanText(removeSpans(text, trigger, match[0])), DueAt: due}, true
	}

	match := absoluteRE.FindStringSubmatch(text)
	if match == nil {
		return Parsed{}, false
	}
	hour, err := strconv.Atoi(match[2])
	if err != nil {
		return Parsed{}, false
	}
	minute := 0
	if match[3] != "" {
		minute, err = strconv.Atoi(match[3])
		if err != nil {
			return Parsed{}, false
		}
	}
	if minute < 0 || minute > 59 {
		return Parsed{}, false
	}

	hour = applyDayPart(hour, strings.ToLower(match[4]))
	if hour < 0 || hour > 23 {
		return Parsed{}, false
	}

	prefix := strings.ToLower(match[1])
	dayOffset := 0
	switch prefix {
	case "besok":
		dayOffset = 1
	case "nanti":
		// "nanti" means later today when the clock still allows it.
		candidate := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), hour, minute, 0, 0, loc)
		if !candidate.After(localNow) {
			dayOffset = 1
		}
	default:
		candidate := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), hour, minute, 0, 0, loc)
		if !candidate.After(localNow) {
			dayOffset = 1
		}
	}

	day := localNow.AddDate(0, 0, dayOffset)
	due := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, loc)
	return Parsed{Text: cleanText(removeSpans(text, trigger, match[0])), DueAt: due}, true
}

// applyDayPart maps Indonesian day parts onto a 24-hour clock. "jam 9" stays
// 09:00; only "malam" (and the other explicit parts) may move it.
func applyDayPart(hour int, part string) int {
	switch part {
	case "malam":
		if hour == 12 {
			return 0
		}
		if hour < 12 {
			return hour + 12
		}
	case "sore":
		if hour < 12 {
			return hour + 12
		}
	case "siang":
		if hour >= 1 && hour <= 10 {
			return hour + 12
		}
	case "pagi":
		if hour == 12 {
			return 0
		}
	}
	return hour
}

func removeSpans(text string, spans ...string) string {
	out := text
	for _, span := range spans {
		if span != "" {
			out = strings.Replace(out, span, " ", 1)
		}
	}
	return out
}

var textPrefixes = []string{"saya", "aku", "untuk", "buat", "bahwa", "tolong", "ya", "dong"}

// cleanText keeps the subject Boss actually asked about, not the command words.
func cleanText(text string) string {
	out := strings.TrimSpace(strings.Trim(text, " \t\n,.;:!?-"))
	for changed := true; changed; {
		changed = false
		for _, prefix := range textPrefixes {
			if rest, ok := trimWordPrefix(out, prefix); ok {
				out = strings.TrimSpace(strings.Trim(rest, " \t\n,.;:!?-"))
				changed = true
			}
		}
	}
	if out == "" {
		return "waktunya"
	}
	return out
}

func trimWordPrefix(s, word string) (string, bool) {
	if len(s) < len(word) || !strings.EqualFold(s[:len(word)], word) {
		return s, false
	}
	rest := s[len(word):]
	if rest == "" {
		return "", true
	}
	if rest[0] != ' ' && rest[0] != ',' {
		return s, false
	}
	return strings.TrimLeft(rest, " ,"), true
}
