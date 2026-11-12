package pricing

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writePrices(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "prices.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write prices: %v", err)
	}
	return path
}

func TestCostUsesTheTableAndNeverGuesses(t *testing.T) {
	path := writePrices(t, `{
	  "deepseek-v4-flash": {"input":0.15,"output":0.60,"cache_read":0.003,"cache_write":null,"unit":"USD_per_1M","source":"test"},
	  "provider-x/model-y": {"input":1,"output":2,"cache_read":0.1,"cache_write":0.2,"unit":"USD_per_1M","source":"test"}
	}`)
	table := New(path)

	// 1M input at $0.15 + 1M output at $0.60 = $0.75.
	got, ok := table.Cost("opencode-go", "deepseek-v4-flash", Counters{Input: 1_000_000, Output: 1_000_000})
	if !ok {
		t.Fatal("known model was not priced")
	}
	if diff := got - 0.75; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("cost = %v, want 0.75", got)
	}

	// 16619 input + 6 output at the deepseek rates.
	got, ok = table.Cost("", "deepseek-v4-flash", Counters{Input: 16619, Output: 6})
	if !ok {
		t.Fatal("turn was not priced")
	}
	want := 16619*0.15/1_000_000 + 6*0.60/1_000_000
	if diff := got - want; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("cost = %v, want %v", got, want)
	}

	// A provider-qualified key wins over a bare one.
	got, ok = table.Cost("provider-x", "model-y", Counters{Input: 1_000_000})
	if !ok || got != 1 {
		t.Fatalf("qualified rate = %v (ok=%v), want 1", got, ok)
	}

	// Unknown model: no cost, not zero.
	if _, ok := table.Cost("", "model-that-does-not-exist", Counters{Input: 10}); ok {
		t.Fatal("unknown model was priced")
	}

	// Cache write has tokens but no published rate: cannot price honestly.
	if _, ok := table.Cost("", "deepseek-v4-flash", Counters{Input: 1, CacheWrite: 10}); ok {
		t.Fatal("a null cache_write rate was treated as zero")
	}
	// Zero cache-write tokens do not need that rate.
	if _, ok := table.Cost("", "deepseek-v4-flash", Counters{Input: 1, CacheWrite: 0}); !ok {
		t.Fatal("a zero counter demanded a rate")
	}
}

func TestMissingFileMeansUnknownNotFree(t *testing.T) {
	table := New(filepath.Join(t.TempDir(), "nope.json"))
	if _, ok := table.Cost("", "deepseek-v4-flash", Counters{Input: 1}); ok {
		t.Fatal("a missing prices file priced a turn")
	}
	if _, err := table.Entries(); err == nil {
		t.Fatal("a missing file was reported as healthy")
	}
}

func TestBossEditsAreSeenWithoutRestart(t *testing.T) {
	path := writePrices(t, `{"m":{"input":1,"output":1,"unit":"USD_per_1M"}}`)
	table := New(path)
	if _, ok := table.Cost("", "m", Counters{Input: 1_000_000}); !ok {
		t.Fatal("initial table did not load")
	}
	// A same-size edit still needs a fresh modtime; make the difference visible.
	later := time.Now().Add(2 * time.Second)
	body := `{"m":{"input":2,"output":1,"unit":"USD_per_1M"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("edit prices: %v", err)
	}
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatalf("touch prices: %v", err)
	}
	got, ok := table.Cost("", "m", Counters{Input: 1_000_000})
	if !ok || got != 2 {
		t.Fatalf("edited cost = %v (ok=%v), want 2", got, ok)
	}
}

func TestFormatUSDKeepsSubCentVisible(t *testing.T) {
	if got := FormatUSD(0.0025); got != "$0.0025" {
		t.Fatalf("sub-cent = %q", got)
	}
	if got := FormatUSD(0.42); got != "$0.42" {
		t.Fatalf("dollars = %q", got)
	}
}
