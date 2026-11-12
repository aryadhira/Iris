package hermes

import (
	"testing"
	"time"
)


// A 90 s silence used to kill a real turn while it was busy running a long tool. Silence is not
// death, so the default must be generous and the operator must be able to raise it without a rebuild.
func TestIdleTimeoutDefaultIsGenerous(t *testing.T) {
	t.Setenv("IRIS_HERMES_IDLE_TIMEOUT", "")
	if defaultIdleTimeout < 5*time.Minute {
		t.Fatalf("default idle timeout = %s, want at least 5m: a long tool call emits nothing", defaultIdleTimeout)
	}
	if got := idleTimeoutFromEnv(); got != defaultIdleTimeout {
		t.Fatalf("idleTimeoutFromEnv() = %s, want the default %s", got, defaultIdleTimeout)
	}
}

func TestIdleTimeoutEnvOverride(t *testing.T) {
	t.Setenv("IRIS_HERMES_IDLE_TIMEOUT", "25m")
	if got := idleTimeoutFromEnv(); got != 25*time.Minute {
		t.Fatalf("idleTimeoutFromEnv() = %s, want 25m", got)
	}

	// A junk or non-positive value must not disable the watchdog: a stream that never ends is
	// also a failure, just a quieter one.
	for _, bad := range []string{"banana", "0", "-5m"} {
		t.Setenv("IRIS_HERMES_IDLE_TIMEOUT", bad)
		if got := idleTimeoutFromEnv(); got != defaultIdleTimeout {
			t.Fatalf("idleTimeoutFromEnv(%q) = %s, want the default %s", bad, got, defaultIdleTimeout)
		}
	}
}
