package config

import (
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.FlushInterval != 20*time.Second || c.ActiveFlowTimeout != time.Minute || c.Retention1m != 7*24*time.Hour {
		t.Errorf("unexpected defaults: flush=%s active=%s retention1m=%s", c.FlushInterval, c.ActiveFlowTimeout, c.Retention1m)
	}
	if c.FoldBelow != 10_000 || c.DBMaxBytes != 2<<30 {
		t.Errorf("unexpected defaults: fold=%d max=%d", c.FoldBelow, c.DBMaxBytes)
	}
}

func TestFlushInterval(t *testing.T) {
	for in, want := range map[string]time.Duration{"60s": time.Minute, "1m": time.Minute, "5s": 5 * time.Second, "2m30s": 150 * time.Second} {
		t.Setenv("NFP_FLUSH_INTERVAL", in)
		c, err := Load()
		if err != nil || c.FlushInterval != want {
			t.Errorf("NFP_FLUSH_INTERVAL=%s: got %v, %v; want %s", in, c.FlushInterval, err, want)
		}
	}
	for _, bad := range []string{"0s", "500ms", "11m", "soon"} {
		t.Setenv("NFP_FLUSH_INTERVAL", bad)
		if _, err := Load(); err == nil {
			t.Errorf("NFP_FLUSH_INTERVAL=%s was accepted", bad)
		}
	}
}

func TestDurationsAndSizes(t *testing.T) {
	t.Setenv("NFP_RETENTION_1M", "14d")
	t.Setenv("NFP_FOLD_BELOW", "64KB")
	t.Setenv("NFP_DB_MAX_SIZE", "1.5GB")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Retention1m != 14*24*time.Hour || c.FoldBelow != 64<<10 || c.DBMaxBytes != 3<<29 {
		t.Errorf("got retention=%s fold=%d max=%d", c.Retention1m, c.FoldBelow, c.DBMaxBytes)
	}
}
