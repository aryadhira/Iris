// Package pricing prices one assistant turn from ~/.config/iris/prices.json.
//
// The file is Boss-editable and is the only source of prices. A model that is
// not in it is not guessed: Cost returns ok=false and the UI says the price is
// unknown. A rate that is null (for example a provider that does not charge for
// cache writes) is not turned into 0 — if the turn reported tokens for that
// counter and the rate is absent, the turn cannot be priced either.
package pricing

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Unit is the only unit the seed script writes. Cost is scaled by 1e6.
const Unit = "USD_per_1M"

// Rate is one model's published rates, per million tokens.
type Rate struct {
	Input      *float64 `json:"input"`
	Output     *float64 `json:"output"`
	CacheRead  *float64 `json:"cache_read"`
	CacheWrite *float64 `json:"cache_write"`
	Unit       string   `json:"unit"`
	Source     string   `json:"source"`
}

// Counters is the five-counter usage of one turn.
type Counters struct {
	Input      int64
	Output     int64
	Total      int64
	CacheRead  int64
	CacheWrite int64
}

// Table is a lazily reloaded prices file. Boss can edit it while irisd runs;
// the next turn sees the new numbers without a restart.
type Table struct {
	path string

	mu     sync.Mutex
	loaded bool
	mod    time.Time
	size   int64
	rates  map[string]Rate
	err    error
}

// New returns a table for path. A missing file is not an error at construction:
// the daemon starts with costs unknown rather than refusing to boot.
func New(path string) *Table {
	return &Table{path: path}
}

// Path reports where the table is read from.
func (t *Table) Path() string { return t.path }

// Entries reports how many models the loaded file knows and the load error, for
// the boot log. It never invents a rate.
func (t *Table) Entries() (int, error) {
	t.reload()
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.rates), t.err
}

func (t *Table) reload() {
	if t == nil || t.path == "" {
		return
	}
	info, err := os.Stat(t.path)
	if err != nil {
		t.mu.Lock()
		if !t.loaded || t.err == nil {
			t.rates = map[string]Rate{}
			t.err = err
			t.loaded = true
		}
		t.mu.Unlock()
		return
	}
	t.mu.Lock()
	fresh := t.loaded && info.ModTime().Equal(t.mod) && info.Size() == t.size
	t.mu.Unlock()
	if fresh {
		return
	}

	raw, readErr := os.ReadFile(t.path)
	parsed := map[string]Rate{}
	var parseErr error
	if readErr != nil {
		parseErr = readErr
	} else if err := json.Unmarshal(raw, &parsed); err != nil {
		parseErr = fmt.Errorf("prices file is not valid JSON: %w", err)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.loaded = true
	t.mod = info.ModTime()
	t.size = info.Size()
	t.err = parseErr
	// A broken edit must not silently zero every price. Keep the last good
	// table and report the error to the caller/log instead.
	if parseErr == nil {
		t.rates = parsed
	} else if t.rates == nil {
		t.rates = map[string]Rate{}
	}
}

// RateFor looks a model up. A provider-qualified key wins over a bare model id,
// so two providers serving the same id can be priced separately when Boss adds
// both.
func (t *Table) RateFor(provider, model string) (Rate, bool) {
	if t == nil {
		return Rate{}, false
	}
	t.reload()
	t.mu.Lock()
	defer t.mu.Unlock()
	model = strings.TrimSpace(model)
	if model == "" {
		return Rate{}, false
	}
	if provider != "" {
		if rate, ok := t.rates[strings.TrimSpace(provider)+"/"+model]; ok {
			return rate, true
		}
	}
	rate, ok := t.rates[model]
	return rate, ok
}

// Cost prices the turn. ok=false means "unknown model" or "a counter with
// tokens has no rate"; cost is nil in that case, never 0.
func (t *Table) Cost(provider, model string, c Counters) (float64, bool) {
	rate, ok := t.RateFor(provider, model)
	if !ok {
		return 0, false
	}
	if rate.Unit != "" && rate.Unit != Unit {
		return 0, false
	}
	total := 0.0
	parts := []struct {
		tokens int64
		rate   *float64
	}{
		{c.Input, rate.Input},
		{c.Output, rate.Output},
		{c.CacheRead, rate.CacheRead},
		{c.CacheWrite, rate.CacheWrite},
	}
	for _, part := range parts {
		if part.tokens == 0 {
			continue
		}
		if part.rate == nil {
			return 0, false
		}
		total += float64(part.tokens) * *part.rate / 1_000_000
	}
	return total, true
}

// FormatUSD is the one money format used by API copy: four decimals below one
// cent so a real $0.0025 never renders as "$0.00".
func FormatUSD(amount float64) string {
	if amount < 0.01 {
		return fmt.Sprintf("$%.4f", amount)
	}
	return fmt.Sprintf("$%.2f", amount)
}

// ErrNoTable is returned by Load for callers that want to distinguish a missing
// prices file from a malformed one.
var ErrNoTable = errors.New("prices file not found")
