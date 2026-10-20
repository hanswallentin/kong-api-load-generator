package config

import (
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Konnect   KonnectConfig  `yaml:"konnect"`
	Gateway   GatewayConfig  `yaml:"gateway"`
	Upstream  UpstreamConfig `yaml:"upstream"`
	Load      LoadConfig     `yaml:"load"`
	Users     UsersConfig    `yaml:"users"`
	Endpoints []Endpoint     `yaml:"endpoints"`
}

type KonnectConfig struct {
	Tool               string   `yaml:"tool"`
	Token              string   `yaml:"token"`
	Region             string   `yaml:"region"`
	ControlPlane       string   `yaml:"control_plane"`
	CreateControlPlane bool     `yaml:"create_control_plane"`
	Tags               []string `yaml:"tags"`
}

type GatewayConfig struct {
	ProxyURL   string  `yaml:"proxy_url"`
	HostHeader *string `yaml:"host_header"`
}

type UpstreamConfig struct {
	DefaultURL string `yaml:"default_url"`
}

type LoadConfig struct {
	Duration       Duration     `yaml:"duration"`
	RampUp         Duration     `yaml:"ramp_up"`
	MaxConcurrency int          `yaml:"max_concurrency"`
	Jitter         float64      `yaml:"jitter"`
	Client         ClientConfig `yaml:"client"`
}

type ClientConfig struct {
	Timeout Duration `yaml:"timeout"`
}

type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Tag == "!!int" {
		var seconds int64
		if err := node.Decode(&seconds); err != nil {
			return err
		}
		*d = Duration(time.Duration(seconds) * time.Second)
		return nil
	}
	var value string
	if err := node.Decode(&value); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Value() time.Duration { return time.Duration(d) }

type UsersConfig struct {
	KeyHeader string      `yaml:"key_header"`
	Groups    []UserGroup `yaml:"groups"`
}

type UserGroup struct {
	Name      string `yaml:"name"`
	Count     int    `yaml:"count"`
	KeyPrefix string `yaml:"key_prefix"`
}

type Endpoint struct {
	Name               string            `yaml:"name"`
	Path               string            `yaml:"path"`
	Methods            []string          `yaml:"methods"`
	UpstreamURL        *string           `yaml:"upstream_url"`
	Frequency          float64           `yaml:"frequency"`
	FrequencyPerMinute float64           `yaml:"frequency_per_minute"`
	ErrorPercentage    float64           `yaml:"error_percentage"`
	ErrorMode          string            `yaml:"error_mode"`
	LatencyMS          LatencyConfig     `yaml:"latency_ms"`
	Auth               string            `yaml:"auth"`
	Identity           *IdentityConfig   `yaml:"identity"`
	Headers            map[string]string `yaml:"headers"`
	Body               string            `yaml:"body"`
	Plugins            []Plugin          `yaml:"plugins"`
}

type LatencyConfig struct {
	Min int `yaml:"min"`
	Max int `yaml:"max"`
}

type IdentityConfig struct {
	Mode   string          `yaml:"mode"`
	User   string          `yaml:"user"`
	Groups []IdentityGroup `yaml:"groups"`
}

type IdentityGroup struct {
	Group  string `yaml:"group"`
	Weight int    `yaml:"weight"`
}

type Plugin struct {
	Name   string         `yaml:"name"`
	Config map[string]any `yaml:"config"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.Konnect.Token == "" {
		cfg.Konnect.Token = os.Getenv("KONNECT_TOKEN")
	}
	return cfg, nil
}

func (e Endpoint) RequestsPerSecond() float64 {
	if e.FrequencyPerMinute > 0 {
		return e.FrequencyPerMinute / 60
	}
	return e.Frequency
}

func (c Config) EffectiveKeyHeader() string {
	if strings.TrimSpace(c.Users.KeyHeader) == "" {
		return "apikey"
	}
	return c.Users.KeyHeader
}
