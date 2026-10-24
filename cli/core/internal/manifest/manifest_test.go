package manifest

import (
	"strings"
	"testing"
)

func TestNewShape(t *testing.T) {
	m := New("com.example.app")
	if m.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", m.SchemaVersion)
	}
	if m.AppID != "com.example.app" {
		t.Errorf("AppID = %q", m.AppID)
	}
	if m.Channels == nil || len(m.Channels) != 0 {
		t.Errorf("Channels should be an initialized empty map, got %#v", m.Channels)
	}
	b, err := m.MarshalPretty()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(b), "\n") || !strings.Contains(string(b), `"schemaVersion": 1`) {
		t.Errorf("unexpected MarshalPretty output: %s", b)
	}
}

func TestUpsertOrderDedupeCap(t *testing.T) {
	m := New("a")
	// insert codes 1..30 oldest-first → sorted desc, capped at 25
	for i := 1; i <= 30; i++ {
		m.Upsert("stable", Release{VersionCode: int64(i), VersionName: "0.0." + itoa(i)})
	}
	h := m.Channels["stable"].History
	if len(h) != MaxHistory {
		t.Fatalf("len = %d, want %d", len(h), MaxHistory)
	}
	if h[0].VersionCode != 30 || h[1].VersionCode != 29 || h[24].VersionCode != 6 {
		t.Errorf("unexpected order: first=%d second=%d last=%d", h[0].VersionCode, h[1].VersionCode, h[24].VersionCode)
	}
	// re-upserting the same versionCode replaces in place (front) and dedupes
	m.Upsert("stable", Release{VersionCode: 30, VersionName: "0.0.30-rc2"})
	h = m.Channels["stable"].History
	if len(h) != MaxHistory {
		t.Fatalf("dedupe failed: len = %d", len(h))
	}
	if h[0].VersionName != "0.0.30-rc2" {
		t.Errorf("expected replaced entry at front, got %q", h[0].VersionName)
	}
	// a brand-new channel appears only after first publish
	if _, ok := m.Channels["beta"]; ok {
		t.Error("beta should not exist before its first publish")
	}
	m.Upsert("beta", Release{VersionCode: 5, VersionName: "0.0.5b"})
	if len(m.Channels["beta"].History) != 1 {
		t.Error("beta history should have one entry")
	}
}

func TestCurrentPrevious(t *testing.T) {
	m := New("a")
	if _, ok := m.Current("stable"); ok {
		t.Error("Current on missing channel should be false")
	}
	m.Upsert("stable", Release{VersionCode: 2})
	m.Upsert("stable", Release{VersionCode: 3})
	cur, ok := m.Current("stable")
	if !ok || cur.VersionCode != 3 {
		t.Fatalf("Current = %v, %v", cur, ok)
	}
	prev, ok := m.Previous("stable")
	if !ok || prev.VersionCode != 2 {
		t.Fatalf("Previous = %v, %v", prev, ok)
	}
}

func TestRollbackTo(t *testing.T) {
	m := New("a")
	for _, c := range []int64{1, 2, 3} {
		m.Upsert("stable", Release{VersionCode: c})
	}
	if err := m.RollbackTo("stable", 1); err != nil {
		t.Fatalf("RollbackTo: %v", err)
	}
	cur, _ := m.Current("stable")
	if cur.VersionCode != 1 {
		t.Errorf("current = %d, want 1", cur.VersionCode)
	}
	if len(m.Channels["stable"].History) != 3 {
		t.Errorf("history lost entries: %d", len(m.Channels["stable"].History))
	}
	if err := m.RollbackTo("stable", 99); err == nil {
		t.Error("missing versionCode should error")
	}
	if err := m.RollbackTo("nope", 1); err == nil {
		t.Error("missing channel should error")
	}
}

func TestPromote(t *testing.T) {
	m := New("a")
	m.Upsert("beta", Release{VersionCode: 10, VersionName: "1.0.0", SHA256: "abc", Rollout: 50})
	m.Upsert("beta", Release{VersionCode: 11, VersionName: "1.1.0"})
	if err := m.Promote("beta", "stable", 11); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	cur, ok := m.Current("stable")
	if !ok || cur.VersionCode != 11 || cur.SHA256 != "" || cur.Rollout != 0 {
		t.Errorf("promoted copy wrong: %+v", cur)
	}
	// source untouched
	if len(m.Channels["beta"].History) != 2 {
		t.Error("beta history must be untouched")
	}
	// promoting the same code twice must not duplicate
	if err := m.Promote("beta", "stable", 11); err != nil {
		t.Fatalf("second Promote: %v", err)
	}
	if len(m.Channels["stable"].History) != 1 {
		t.Errorf("stable history = %d, want 1 (dedupe)", len(m.Channels["stable"].History))
	}
	if err := m.Promote("beta", "beta", 10); err == nil {
		t.Error("from == to should error")
	}
	if err := m.Promote("beta", "alpha", 77); err == nil {
		t.Error("missing versionCode should error")
	}
}

func TestLoad(t *testing.T) {
	m, err := Load([]byte(`{"schemaVersion":1,"appId":"a","updatedAt":"2026-01-01T00:00:00Z","channels":{"stable":{"history":[{"versionCode":7,"versionName":"1.0"}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	cur, ok := m.Current("stable")
	if !ok || cur.VersionCode != 7 || cur.VersionName != "1.0" {
		t.Errorf("roundtrip failed: %+v ok=%v", cur, ok)
	}
	// missing schemaVersion is tolerated and normalized
	if _, err := Load([]byte(`{"appId":"a"}`)); err != nil {
		t.Errorf("lenient load failed: %v", err)
	}
	// unsupported schema
	if _, err := Load([]byte(`{"schemaVersion":2,"appId":"a"}`)); err == nil {
		t.Error("schemaVersion 2 should error")
	}
	// bad json
	if _, err := Load([]byte(`nope`)); err == nil {
		t.Error("invalid json should error")
	}
	// channels null → initialized
	m2, err := Load([]byte(`{"schemaVersion":1,"appId":"a","channels":null}`))
	if err != nil || m2.Channels == nil {
		t.Errorf("channels should be initialized, err=%v", err)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
