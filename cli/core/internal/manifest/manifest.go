// Package manifest implements the release manifest (contract §1): the JSON
// shape stored at .zen/manifest.json in the user's repository plus the
// history rules (newest first, dedupe by versionCode, cap 25 entries).
package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// MaxHistory caps every channel's history length.
const MaxHistory = 25

// SchemaVersion is the current manifest schema (contract §1).
const SchemaVersion = 1

// Manifest is the root document.
type Manifest struct {
	SchemaVersion int                `json:"schemaVersion"`
	AppID         string             `json:"appId"`
	UpdatedAt     string             `json:"updatedAt"`
	Channels      map[string]Channel `json:"channels"`
}

// Channel holds one channel's history, newest first.
type Channel struct {
	History []Release `json:"history"`
}

// Release is one published release (contract §1 ZenRelease).
type Release struct {
	VersionName    string `json:"versionName"`
	VersionCode    int64  `json:"versionCode"`
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size"`
	ReleaseNotes   string `json:"releaseNotes"`
	PublishedAt    string `json:"publishedAt"`
	Rollout        int    `json:"rollout"`
	MinVersionCode int64  `json:"minVersionCode"`
	Assets         Assets `json:"assets"`
}

// Assets points at the uploaded APK copies.
type Assets struct {
	GitHub string `json:"github"`
	R2     string `json:"r2,omitempty"`
}

// New creates a fresh manifest for appID. Channels stay empty until the
// first publish: a channel that never had a release simply does not exist.
func New(appID string) *Manifest {
	return &Manifest{
		SchemaVersion: SchemaVersion,
		AppID:         appID,
		UpdatedAt:     now(),
		Channels:      map[string]Channel{},
	}
}

// Load parses and validates a manifest document.
func Load(data []byte) (*Manifest, error) {
	m := &Manifest{}
	if err := json.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("manifest is not valid json: %w", err)
	}
	if m.SchemaVersion == 0 {
		m.SchemaVersion = SchemaVersion
	}
	if m.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("unsupported manifest schemaVersion %d (want %d)", m.SchemaVersion, SchemaVersion)
	}
	if m.AppID == "" {
		return nil, errors.New("manifest has no appId")
	}
	if m.Channels == nil {
		m.Channels = map[string]Channel{}
	}
	return m, nil
}

// MarshalPretty renders the manifest as 2-space-indented JSON + "\n".
func (m *Manifest) MarshalPretty() ([]byte, error) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Current returns the channel's newest release (history[0]).
func (m *Manifest) Current(channel string) (*Release, bool) {
	ch, ok := m.Channels[channel]
	if !ok || len(ch.History) == 0 {
		return nil, false
	}
	rel := ch.History[0]
	return &rel, true
}

// Previous returns the channel's second-newest release (history[1]).
func (m *Manifest) Previous(channel string) (*Release, bool) {
	ch, ok := m.Channels[channel]
	if !ok || len(ch.History) < 2 {
		return nil, false
	}
	rel := ch.History[1]
	return &rel, true
}

// Find returns the release with the given versionCode in the channel.
func (m *Manifest) Find(channel string, versionCode int64) (*Release, bool) {
	ch, ok := m.Channels[channel]
	if !ok {
		return nil, false
	}
	for i := range ch.History {
		if ch.History[i].VersionCode == versionCode {
			rel := ch.History[i]
			return &rel, true
		}
	}
	return nil, false
}

// FindByName returns the newest release with the given versionName.
func (m *Manifest) FindByName(channel, versionName string) (*Release, bool) {
	ch, ok := m.Channels[channel]
	if !ok {
		return nil, false
	}
	for i := range ch.History { // history is newest first
		if ch.History[i].VersionName == versionName {
			rel := ch.History[i]
			return &rel, true
		}
	}
	return nil, false
}

// Upsert inserts rel at the front of the channel, dropping any existing
// entry with the same versionCode, keeping the history sorted descending by
// versionCode and capped at MaxHistory entries.
func (m *Manifest) Upsert(channel string, rel Release) {
	ch := m.Channels[channel] // zero value when the channel is new
	out := make([]Release, 0, len(ch.History)+1)
	out = append(out, rel)
	for _, r := range ch.History {
		if r.VersionCode != rel.VersionCode {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].VersionCode > out[j].VersionCode })
	if len(out) > MaxHistory {
		out = out[:MaxHistory]
	}
	ch.History = out
	m.Channels[channel] = ch
	m.touch()
}

// RollbackTo moves the release with the given versionCode to the front of
// the channel history, making it the current release.
func (m *Manifest) RollbackTo(channel string, versionCode int64) error {
	ch, ok := m.Channels[channel]
	if !ok {
		return fmt.Errorf("channel %q does not exist", channel)
	}
	idx := -1
	for i := range ch.History {
		if ch.History[i].VersionCode == versionCode {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("no release with versionCode %d in channel %q", versionCode, channel)
	}
	rest := make([]Release, 0, len(ch.History))
	rest = append(rest, ch.History[idx])
	for i, r := range ch.History {
		if i != idx {
			rest = append(rest, r)
		}
	}
	ch.History = rest
	m.Channels[channel] = ch
	m.touch()
	return nil
}

// Promote deep-copies the release identified by versionCode from the from
// channel to the front of the to channel.
func (m *Manifest) Promote(from, to string, versionCode int64) error {
	if from == to {
		return fmt.Errorf("from and to channels are the same (%q)", from)
	}
	rel, ok := m.Find(from, versionCode)
	if !ok {
		if _, exists := m.Channels[from]; !exists {
			return fmt.Errorf("channel %q does not exist", from)
		}
		return fmt.Errorf("no release with versionCode %d in channel %q", versionCode, from)
	}
	m.Upsert(to, *rel) // *rel is a deep copy: Release holds only value fields
	return nil
}

// ChannelNames lists the channels that exist, canonical order first
// (stable, beta, alpha) and extras sorted after them.
func (m *Manifest) ChannelNames() []string {
	canonical := []string{"stable", "beta", "alpha"}
	var out, rest []string
	seen := map[string]bool{}
	for _, c := range canonical {
		if _, ok := m.Channels[c]; ok {
			out = append(out, c)
			seen[c] = true
		}
	}
	for c := range m.Channels {
		if !seen[c] {
			rest = append(rest, c)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

func (m *Manifest) touch() { m.UpdatedAt = now() }

func now() string { return time.Now().UTC().Format(time.RFC3339) }
