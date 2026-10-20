package kong

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/kong/kong-loadgen/src/config"
	"gopkg.in/yaml.v3"
)

const ManagedTag = "kong-loadgen"

type Generated struct {
	DeckState     []byte
	DeckTeardown  []byte
	KongctlConfig []byte
	ControlPlane  []byte
}

type deckState struct {
	FormatVersion string     `yaml:"_format_version"`
	Info          deckInfo   `yaml:"_info"`
	Services      []service  `yaml:"services"`
	Plugins       []plugin   `yaml:"plugins,omitempty"`
	Consumers     []consumer `yaml:"consumers,omitempty"`
}

type deckInfo struct {
	SelectTags []string `yaml:"select_tags"`
}

type service struct {
	Name   string   `yaml:"name"`
	URL    string   `yaml:"url"`
	Tags   []string `yaml:"tags"`
	Routes []route  `yaml:"routes"`
}

type route struct {
	Name      string   `yaml:"name"`
	Paths     []string `yaml:"paths"`
	Methods   []string `yaml:"methods"`
	StripPath bool     `yaml:"strip_path"`
	Tags      []string `yaml:"tags"`
}

type plugin struct {
	Name   string         `yaml:"name"`
	Route  string         `yaml:"route,omitempty"`
	Config map[string]any `yaml:"config,omitempty"`
	Tags   []string       `yaml:"tags"`
}

type consumer struct {
	Username          string       `yaml:"username"`
	Tags              []string     `yaml:"tags"`
	KeyAuthCredential []credential `yaml:"keyauth_credentials"`
}

type credential struct {
	Key  string   `yaml:"key"`
	Tags []string `yaml:"tags"`
}

type kongctlFile struct {
	ControlPlanes []controlPlane `yaml:"control_planes"`
}

type controlPlane struct {
	Ref         string            `yaml:"ref"`
	Name        string            `yaml:"name,omitempty"`
	ClusterType string            `yaml:"cluster_type,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty"`
	Kongctl     *kongctlMetadata  `yaml:"kongctl,omitempty"`
	External    *externalResource `yaml:"_external,omitempty"`
	Deck        *deckIntegration  `yaml:"_deck,omitempty"`
}

type kongctlMetadata struct {
	Namespace string `yaml:"namespace"`
}

type externalResource struct {
	Selector externalSelector `yaml:"selector"`
}

type externalSelector struct {
	MatchFields map[string]string `yaml:"matchFields"`
}

type deckIntegration struct {
	Files []string `yaml:"files"`
	Flags []string `yaml:"flags"`
}

func Generate(cfg config.Config) (Generated, error) {
	tags := managedTags(cfg.Konnect.Tags)
	state := deckState{
		FormatVersion: "3.0",
		Info:          deckInfo{SelectTags: []string{ManagedTag}},
	}
	for _, endpoint := range cfg.Endpoints {
		upstream := cfg.Upstream.DefaultURL
		if endpoint.UpstreamURL != nil {
			upstream = *endpoint.UpstreamURL
		}
		serviceURL, err := joinServiceURL(upstream, endpoint.Path)
		if err != nil {
			return Generated{}, fmt.Errorf("endpoint %q upstream URL: %w", endpoint.Name, err)
		}
		methods := endpoint.Methods
		if len(methods) == 0 {
			methods = []string{"GET"}
		}
		routeName := endpoint.Name + "-route"
		state.Services = append(state.Services, service{
			Name: endpoint.Name,
			URL:  serviceURL,
			Tags: tags,
			Routes: []route{{
				Name:      routeName,
				Paths:     []string{endpoint.Path},
				Methods:   methods,
				StripPath: false,
				Tags:      tags,
			}},
		})
		if endpoint.Auth == "key-auth" {
			state.Plugins = append(state.Plugins, plugin{
				Name:  "key-auth",
				Route: routeName,
				Config: map[string]any{
					"key_names":     []string{cfg.EffectiveKeyHeader()},
					"key_in_header": true,
					"key_in_query":  false,
					"key_in_body":   false,
				},
				Tags: tags,
			})
		}
		for _, configured := range endpoint.Plugins {
			state.Plugins = append(state.Plugins, plugin{
				Name:   configured.Name,
				Route:  routeName,
				Config: configured.Config,
				Tags:   tags,
			})
		}
	}
	for _, group := range cfg.Users.Groups {
		for index := 1; index <= group.Count; index++ {
			username := fmt.Sprintf("%s-%02d", group.Name, index)
			userTags := managedTags(append(append([]string(nil), cfg.Konnect.Tags...), group.Name))
			state.Consumers = append(state.Consumers, consumer{
				Username: username,
				Tags:     userTags,
				KeyAuthCredential: []credential{{
					Key:  fmt.Sprintf("%s-%02d", group.KeyPrefix, index),
					Tags: userTags,
				}},
			})
		}
	}
	deckBytes, err := marshalYAML(state)
	if err != nil {
		return Generated{}, fmt.Errorf("encode decK state: %w", err)
	}
	teardownBytes, err := marshalYAML(deckState{
		FormatVersion: "3.0",
		Info:          deckInfo{SelectTags: []string{ManagedTag}},
	})
	if err != nil {
		return Generated{}, fmt.Errorf("encode decK teardown state: %w", err)
	}
	control := controlPlane{
		Ref:  cfg.Konnect.ControlPlane,
		Deck: &deckIntegration{Files: []string{"kong.yaml"}, Flags: []string{"--select-tag=" + ManagedTag}},
	}
	if cfg.Konnect.CreateControlPlane {
		control.Name = cfg.Konnect.ControlPlane
		control.ClusterType = "CLUSTER_TYPE_CONTROL_PLANE"
		control.Labels = map[string]string{"managed-by": ManagedTag}
		control.Kongctl = &kongctlMetadata{Namespace: ManagedTag}
	} else {
		control.External = &externalResource{Selector: externalSelector{MatchFields: map[string]string{"name": cfg.Konnect.ControlPlane}}}
	}
	kongctlBytes, err := marshalYAML(kongctlFile{ControlPlanes: []controlPlane{control}})
	if err != nil {
		return Generated{}, fmt.Errorf("encode kongctl config: %w", err)
	}
	control.Deck = nil
	controlPlaneBytes, err := marshalYAML(kongctlFile{ControlPlanes: []controlPlane{control}})
	if err != nil {
		return Generated{}, fmt.Errorf("encode control-plane teardown config: %w", err)
	}
	return Generated{DeckState: deckBytes, DeckTeardown: teardownBytes, KongctlConfig: kongctlBytes, ControlPlane: controlPlaneBytes}, nil
}

func marshalYAML(value any) ([]byte, error) {
	var output strings.Builder
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return []byte(output.String()), nil
}

func joinServiceURL(upstream, routePath string) (string, error) {
	parsed, err := url.Parse(upstream)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("must be an absolute URL")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = ""
		return strings.TrimRight(parsed.String(), "/"), nil
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return strings.TrimRight(parsed.String(), "/"), nil
}

func managedTags(configured []string) []string {
	seen := map[string]bool{}
	tags := make([]string, 0, len(configured)+1)
	for _, tag := range append(append([]string(nil), configured...), ManagedTag) {
		if tag != "" && !seen[tag] {
			seen[tag] = true
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	return tags
}
