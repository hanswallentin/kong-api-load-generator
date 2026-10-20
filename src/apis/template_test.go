package apis

import (
	"strings"
	"testing"

	"github.com/kong/kong-loadgen/src/config"
	"gopkg.in/yaml.v3"
)

func TestTemplateDescriptionIsAboutFiftyWords(t *testing.T) {
	description, _ := Template(config.Endpoint{Name: "orders-api", Path: "/orders", Methods: []string{"GET", "POST"}})
	words := len(strings.Fields(description))
	if words < 45 || words > 60 {
		t.Fatalf("description has %d words, want about 50: %q", words, description)
	}
}

func TestTemplateSpecIsOneToTwoKilobytesAndIncludesPath(t *testing.T) {
	endpoints := []config.Endpoint{
		{Name: "public-catalog", Path: "/catalog", Methods: []string{"GET"}},
		{Name: "orders-api", Path: "/orders", Methods: []string{"GET", "POST"}},
		{Name: "checkout-api", Path: "/checkout", Methods: []string{"POST"}},
	}
	for _, endpoint := range endpoints {
		_, spec := Template(endpoint)
		if size := len(spec); size < 1000 || size > 2000 {
			t.Fatalf("%s spec is %d bytes, want 1000-2000", endpoint.Name, size)
		}
		var document map[string]any
		if err := yaml.Unmarshal([]byte(spec), &document); err != nil {
			t.Fatalf("%s spec is not valid YAML: %v", endpoint.Name, err)
		}
		paths := document["paths"].(map[string]any)
		if _, ok := paths[endpoint.Path]; !ok {
			t.Fatalf("%s spec does not include path %s", endpoint.Name, endpoint.Path)
		}
	}
}

func TestResolveFillsMissingEndpointsWithTemplates(t *testing.T) {
	cfg := config.Config{Endpoints: []config.Endpoint{
		{Name: "orders-api", Path: "/orders", Methods: []string{"GET"}},
		{Name: "catalog", Path: "/catalog", Methods: []string{"GET"}},
	}}
	file := File{APIs: []Entry{{Endpoint: "orders-api", Description: "Hand-written.", Spec: "openapi: 3.0.3\n"}}}
	apis, notes := Resolve(cfg, file)
	if len(apis) != 2 || apis[0].Description != "Hand-written." {
		t.Fatalf("resolved %+v; the existing entry should win", apis)
	}
	if apis[1].Name != "catalog" || !strings.Contains(apis[1].Spec, "/catalog") {
		t.Fatalf("missing endpoint not templated: %+v", apis[1])
	}
	if len(notes) != 1 {
		t.Fatalf("notes = %v, want one notice for the templated endpoint", notes)
	}
}
