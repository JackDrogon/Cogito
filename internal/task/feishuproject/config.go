package feishuproject

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/JackDrogon/Cogito/internal/store"
)

const (
	defaultWorkItemTypeKey = "story"
	defaultPageSize        = 100
	defaultPollInterval    = time.Minute
	defaultStateFile       = store.DefaultStateRoot + "/feishu/state.json"
	defaultOutputFile      = store.DefaultStateRoot + "/feishu/stories.json"
)

// Config is the resolved [meegle] configuration used by Service. Repos comes
// from the sibling top-level [repos] TOML table and is included on Config so
// downstream code can resolve a story's repo field to a local path without
// re-parsing the file.
type Config struct {
	BaseURL         string
	PluginID        string
	PluginSecret    string
	UserKey         string
	ProjectKey      string
	AuthMode        string
	WorkItemTypeKey string
	PageSize        int64
	Poll            PollConfig
	Repos           map[string]string
}

// PollConfig governs Watch mode behavior.
type PollConfig struct {
	Interval   time.Duration
	StateFile  string
	OutputFile string
}

// LoadConfig reads a TOML file and returns the resolved Config. The file
// must contain a [meegle] section; [repos] is optional and absent maps to
// an empty Config.Repos.
func LoadConfig(path string) (Config, error) {
	var raw rawConfig
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return Config{}, fmt.Errorf("feishuproject: decode %s: %w", path, err)
	}

	cfg, err := raw.Meegle.toConfig()
	if err != nil {
		return Config{}, fmt.Errorf("feishuproject: %w", err)
	}

	cfg.Repos = normalizeRepos(raw.Repos)

	return cfg, nil
}

// normalizeRepos trims keys/values and drops empty entries so downstream
// lookups don't have to defensively re-trim.
func normalizeRepos(raw map[string]string) map[string]string {
	if len(raw) == 0 {
		return nil
	}

	repos := make(map[string]string, len(raw))

	for key, value := range raw {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		if key == "" || value == "" {
			continue
		}

		repos[key] = value
	}

	if len(repos) == 0 {
		return nil
	}

	return repos
}

// rawConfig mirrors the on-disk TOML layout. Keeping it private lets the
// public Config evolve independently of the file schema.
type rawConfig struct {
	Meegle rawMeegle         `toml:"meegle"`
	Repos  map[string]string `toml:"repos"`
}

type rawMeegle struct {
	BaseURL         string  `toml:"base_url"`
	PluginID        string  `toml:"plugin_id"`
	PluginSecret    string  `toml:"plugin_secret"`
	UserKey         string  `toml:"user_key"`
	ProjectKey      string  `toml:"project_key"`
	AuthMode        string  `toml:"auth_mode"`
	WorkItemTypeKey string  `toml:"work_item_type_key"`
	PageSize        int64   `toml:"page_size"`
	Poll            rawPoll `toml:"poll"`
}

type rawPoll struct {
	Interval   string `toml:"interval"`
	StateFile  string `toml:"state_file"`
	OutputFile string `toml:"output_file"`
}

func (r rawMeegle) toConfig() (Config, error) {
	cfg := Config{
		BaseURL:         strings.TrimSpace(r.BaseURL),
		PluginID:        strings.TrimSpace(r.PluginID),
		PluginSecret:    strings.TrimSpace(r.PluginSecret),
		UserKey:         strings.TrimSpace(r.UserKey),
		ProjectKey:      strings.TrimSpace(r.ProjectKey),
		AuthMode:        strings.TrimSpace(r.AuthMode),
		WorkItemTypeKey: strings.TrimSpace(r.WorkItemTypeKey),
		PageSize:        r.PageSize,
	}

	cfg.applyDefaults()

	poll, err := r.Poll.toConfig()
	if err != nil {
		return Config{}, err
	}

	cfg.Poll = poll

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (r rawPoll) toConfig() (PollConfig, error) {
	poll := PollConfig{
		StateFile:  strings.TrimSpace(r.StateFile),
		OutputFile: strings.TrimSpace(r.OutputFile),
	}

	interval := strings.TrimSpace(r.Interval)
	if interval != "" {
		parsed, err := time.ParseDuration(interval)
		if err != nil {
			return PollConfig{}, fmt.Errorf("meegle.poll.interval: %w", err)
		}

		poll.Interval = parsed
	}

	poll.applyDefaults()

	return poll, nil
}

func (c *Config) applyDefaults() {
	if c.WorkItemTypeKey == "" {
		c.WorkItemTypeKey = defaultWorkItemTypeKey
	}

	if c.PageSize <= 0 {
		c.PageSize = defaultPageSize
	}
}

func (p *PollConfig) applyDefaults() {
	if p.Interval <= 0 {
		p.Interval = defaultPollInterval
	}

	if p.StateFile == "" {
		p.StateFile = defaultStateFile
	}

	if p.OutputFile == "" {
		p.OutputFile = defaultOutputFile
	}
}

// Validate ensures the credentials needed to talk to the API are present.
// Missing values fail fast so the CLI never silently no-ops.
func (c *Config) Validate() error {
	var missing []string
	if c.BaseURL == "" {
		missing = append(missing, "base_url")
	}

	if c.PluginID == "" {
		missing = append(missing, "plugin_id")
	}

	if c.PluginSecret == "" {
		missing = append(missing, "plugin_secret")
	}

	if c.UserKey == "" {
		missing = append(missing, "user_key")
	}

	if c.ProjectKey == "" {
		missing = append(missing, "project_key")
	}

	if len(missing) > 0 {
		return errors.New("meegle: required fields missing: " + strings.Join(missing, ", "))
	}

	return nil
}
