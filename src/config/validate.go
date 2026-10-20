package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type ValidationError struct {
	Line    int
	Message string
}

func (e ValidationError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Message) }

func Validate(cfg Config, root *yaml.Node) []ValidationError {
	var problems []ValidationError
	add := func(node *yaml.Node, message string) {
		line := 1
		if node != nil && node.Line > 0 {
			line = node.Line
		}
		problems = append(problems, ValidationError{Line: line, Message: message})
	}
	rootMap := mappingValue(root)
	konnectNode := mappingValue(rootMap["konnect"])
	gatewayNode := mappingValue(rootMap["gateway"])
	upstreamNode := mappingValue(rootMap["upstream"])
	loadNode := mappingValue(rootMap["load"])
	usersNode := mappingValue(rootMap["users"])

	if cfg.Konnect.Token == "" {
		add(konnectNode["token"], "Konnect token is required; set KONNECT_TOKEN or konnect.token")
	}
	if cfg.Konnect.Tool != "kongctl" && cfg.Konnect.Tool != "deck" {
		add(konnectNode["tool"], "konnect.tool must be kongctl or deck")
	}
	if !oneOf(cfg.Konnect.Region, "us", "eu", "au", "me", "in") {
		add(konnectNode["region"], "konnect.region must be one of us, eu, au, me, in")
	}
	if strings.TrimSpace(cfg.Konnect.ControlPlane) == "" {
		add(konnectNode["control_plane"], "konnect.control_plane is required")
	}
	checkURL := func(node *yaml.Node, raw, name string) {
		parsed, err := url.ParseRequestURI(raw)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			add(node, name+" must be an absolute URL")
		}
	}
	checkURL(gatewayNode["proxy_url"], cfg.Gateway.ProxyURL, "gateway.proxy_url")
	checkURL(upstreamNode["default_url"], cfg.Upstream.DefaultURL, "upstream.default_url")
	if cfg.Load.Duration.Value() < 0 || cfg.Load.RampUp.Value() < 0 {
		add(loadNode["duration"], "load.duration and load.ramp_up cannot be negative")
	}
	if cfg.Load.MaxConcurrency <= 0 {
		add(loadNode["max_concurrency"], "load.max_concurrency must be greater than zero")
	}
	if cfg.Load.Jitter < 0 || cfg.Load.Jitter > 1 {
		add(loadNode["jitter"], "load.jitter must be between 0 and 1")
	}
	if cfg.Load.Client.Timeout.Value() <= 0 {
		add(mappingValue(loadNode["client"])["timeout"], "load.client.timeout must be greater than zero")
	}
	if !validHeaderName(cfg.EffectiveKeyHeader()) {
		add(usersNode["key_header"], "users.key_header must be a valid HTTP header name")
	}

	groupsByName := map[string]UserGroup{}
	prefixes := map[string]bool{}
	groupNodes := sequenceValue(usersNode["groups"])
	for i, group := range cfg.Users.Groups {
		var node *yaml.Node
		if i < len(groupNodes) {
			node = groupNodes[i]
		}
		if group.Name == "" {
			add(mappingValue(node)["name"], "user group name is required")
		}
		if _, exists := groupsByName[group.Name]; exists {
			add(mappingValue(node)["name"], "duplicate user group name: "+group.Name)
		}
		groupsByName[group.Name] = group
		if group.Count <= 0 {
			add(mappingValue(node)["count"], "user group count must be greater than zero")
		}
		if group.KeyPrefix == "" {
			add(mappingValue(node)["key_prefix"], "user group key_prefix is required")
		} else if prefixes[group.KeyPrefix] {
			add(mappingValue(node)["key_prefix"], "user group key_prefix values must be unique")
		}
		prefixes[group.KeyPrefix] = true
	}

	names, paths := map[string]bool{}, map[string]bool{}
	endpointNodes := sequenceValue(rootMap["endpoints"])
	for i, endpoint := range cfg.Endpoints {
		var node *yaml.Node
		if i < len(endpointNodes) {
			node = endpointNodes[i]
		}
		fields := mappingValue(node)
		if endpoint.Name == "" {
			add(firstNode(fields["name"], node), "endpoint name is required")
		} else if names[endpoint.Name] {
			add(fields["name"], "duplicate endpoint name: "+endpoint.Name)
		}
		names[endpoint.Name] = true
		if !strings.HasPrefix(endpoint.Path, "/") {
			add(firstNode(fields["path"], node), "endpoint path must start with /")
		}
		if paths[endpoint.Path] {
			add(fields["path"], "duplicate endpoint path: "+endpoint.Path)
		}
		paths[endpoint.Path] = true
		if endpoint.Frequency > 0 && endpoint.FrequencyPerMinute > 0 || endpoint.Frequency <= 0 && endpoint.FrequencyPerMinute <= 0 {
			add(firstNode(fields["frequency"], fields["frequency_per_minute"], node), "set exactly one of frequency or frequency_per_minute to a value greater than zero")
		}
		if endpoint.ErrorPercentage < 0 || endpoint.ErrorPercentage > 100 {
			add(fields["error_percentage"], "error_percentage must be between 0 and 100")
		}
		if !oneOf(endpoint.Auth, "none", "key-auth") {
			add(fields["auth"], "endpoint auth must be none or key-auth")
		}
		if endpoint.Auth == "key-auth" && endpoint.Identity == nil {
			add(firstNode(fields["identity"], node), "key-auth endpoints require an identity block")
		}
		if endpoint.Auth == "none" && endpoint.Identity != nil {
			add(fields["identity"], "auth: none endpoints must not have an identity block")
		}
		if endpoint.ErrorMode != "" && !oneOf(endpoint.ErrorMode, "upstream", "client", "mixed") {
			add(fields["error_mode"], "error_mode must be upstream, client, or mixed")
		}
		if endpoint.LatencyMS.Min < 0 || endpoint.LatencyMS.Max < endpoint.LatencyMS.Min {
			add(fields["latency_ms"], "latency_ms requires 0 <= min <= max")
		}
		if endpoint.Identity != nil {
			validateIdentity(*endpoint.Identity, fields["identity"], groupsByName, add)
		}
	}
	if len(cfg.Endpoints) == 0 {
		add(rootMap["endpoints"], "at least one endpoint is required")
	}
	return problems
}

func firstNode(nodes ...*yaml.Node) *yaml.Node {
	for _, node := range nodes {
		if node != nil {
			return node
		}
	}
	return nil
}

func validateIdentity(identity IdentityConfig, node *yaml.Node, groups map[string]UserGroup, add func(*yaml.Node, string)) {
	fields := mappingValue(node)
	if !oneOf(identity.Mode, "single-user", "weighted-groups", "round-robin") {
		add(fields["mode"], "identity.mode must be single-user, weighted-groups, or round-robin")
		return
	}
	if identity.Mode == "single-user" {
		groupName := ""
		var group UserGroup
		for candidateName, candidate := range groups {
			if strings.HasPrefix(identity.User, candidateName+"-") && len(candidateName) > len(groupName) {
				groupName = candidateName
				group = candidate
			}
		}
		if groupName == "" {
			add(fields["user"], "identity.user must name an existing generated user")
		} else if !validUserNumber(strings.TrimPrefix(identity.User, groupName+"-"), group.Count) {
			add(fields["user"], "identity.user must name a generated user within its group's count")
		}
		return
	}
	if len(identity.Groups) == 0 {
		add(fields["groups"], "identity.groups must list at least one user group")
		return
	}
	groupNodes := sequenceValue(fields["groups"])
	sum, seen := 0, map[string]bool{}
	for i, ref := range identity.Groups {
		var refNode *yaml.Node
		if i < len(groupNodes) {
			refNode = groupNodes[i]
		}
		refFields := mappingValue(refNode)
		group, exists := groups[ref.Group]
		if !exists {
			add(refFields["group"], "identity references unknown user group: "+ref.Group)
		} else if group.Count <= 0 {
			add(refFields["group"], "identity references a group with no generated users: "+ref.Group)
		}
		if seen[ref.Group] {
			add(refFields["group"], "identity group may only be listed once: "+ref.Group)
		}
		seen[ref.Group] = true
		if identity.Mode == "weighted-groups" {
			if ref.Weight <= 0 {
				add(refFields["weight"], "weighted-groups weights must be greater than zero")
			}
			sum += ref.Weight
		} else if ref.Weight != 0 {
			add(refFields["weight"], "round-robin does not use weights")
		}
	}
	if identity.Mode == "weighted-groups" && sum <= 0 {
		add(fields["groups"], "weighted-groups weights must sum to a positive value")
	}
}

func validUserNumber(value string, count int) bool {
	index, err := strconv.Atoi(value)
	if err != nil {
		return false
	}
	return index > 0 && index <= count
}

func validHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)) {
			return false
		}
	}
	return true
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func mappingValue(node *yaml.Node) map[string]*yaml.Node {
	result := map[string]*yaml.Node{}
	if node == nil || node.Kind != yaml.MappingNode {
		return result
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		result[node.Content[i].Value] = node.Content[i+1]
	}
	return result
}

func sequenceValue(node *yaml.Node) []*yaml.Node {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil
	}
	return node.Content
}
