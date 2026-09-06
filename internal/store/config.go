package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Config holds daemon preferences that outlive a session.
//
// It lives beside the session rather than in the plugin's shell.json entry
// because the daemon needs it while running headless — the background cookie
// sync must know which browser profile to read, with no panel open.
type Config struct {
	// BrowserProfile is the profile name to take Google cookies from, as
	// reported by the browser scan (e.g. "Chrome / Profile 1"). Empty means
	// choose automatically.
	BrowserProfile string `json:"browserProfile,omitempty"`

	// GiphyAPIKey enables GIF search. GIPHY issues free keys; without one the
	// GIF picker explains how to get it rather than failing silently.
	GiphyAPIKey string `json:"giphyApiKey,omitempty"`
}

// ConfigStore reads and writes the config file.
type ConfigStore struct {
	path string

	mu     sync.RWMutex
	loaded Config
}

// ConfigFile is where preferences are persisted.
func (p *Paths) ConfigFile() string { return filepath.Join(p.Data, "config.json") }

// NewConfigStore loads the config, treating a missing or corrupt file as
// defaults rather than an error: a bad config must never stop the daemon.
func NewConfigStore(path string) *ConfigStore {
	cs := &ConfigStore{path: path}
	f, err := os.Open(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return cs
		}
		return cs
	}
	defer f.Close()
	_ = json.NewDecoder(f).Decode(&cs.loaded)
	return cs
}

// Get returns a copy of the current config.
func (c *ConfigStore) Get() Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loaded
}

// SetBrowserProfile records the chosen profile and persists it atomically.
func (c *ConfigStore) SetBrowserProfile(name string) error {
	c.mu.Lock()
	c.loaded.BrowserProfile = name
	c.mu.Unlock()
	return c.save()
}

// SetGiphyAPIKey stores the GIF search key.
func (c *ConfigStore) SetGiphyAPIKey(key string) error {
	c.mu.Lock()
	c.loaded.GiphyAPIKey = key
	c.mu.Unlock()
	return c.save()
}

func (c *ConfigStore) save() error {
	c.mu.RLock()
	current := c.loaded
	c.mu.RUnlock()

	tmp := c.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(f).Encode(&current); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, c.path)
}
