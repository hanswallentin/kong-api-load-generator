package kong

import (
	"strings"
	"testing"

	"github.com/kong/kong-loadgen/src/apis"
	"github.com/kong/kong-loadgen/src/config"
	"gopkg.in/yaml.v3"
)

func apiTestItems() []apis.API {
	return []apis.API{
		{Name: "orders-api", Description: "Orders.", Spec: "openapi: 3.0.3\n"},
		{Name: "catalog", Description: "Catalog.", Spec: "openapi: 3.0.3\n"},
	}
}

func TestAPIsManifestReferencesControlPlaneAndServicesByName(t *testing.T) {
	cfg := config.Config{Konnect: config.KonnectConfig{ControlPlane: "loadgen-demo"}}
	manifest, err := APIsManifest(cfg, apiTestItems())
	if err != nil {
		t.Fatal(err)
	}
	text := string(manifest)
	for _, want := range []string{
		"namespace: " + APIsNamespace,
		"content: |",
		"id: !ref orders-api-svc#id",
		"control_plane_id: !ref loadgen-demo#id",
		`version: "1.0.0"`,
		"managed-by: kong-loadgen",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("manifest missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "openapi: 3.0.3") {
		t.Fatalf("spec is not inline in the manifest:\n%s", text)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(manifest, &parsed); err != nil {
		t.Fatalf("manifest is not valid YAML: %v", err)
	}
}

func TestAPIsDeleteManifestHasNoImplementations(t *testing.T) {
	manifest, err := APIsDeleteManifest([]string{"orders-api"})
	if err != nil {
		t.Fatal(err)
	}
	text := string(manifest)
	if strings.Contains(text, "implementations") || strings.Contains(text, "control_planes") {
		t.Fatalf("delete manifest should name the API only, got:\n%s", text)
	}
	if !strings.Contains(text, "name: orders-api") || !strings.Contains(text, "namespace: "+APIsNamespace) {
		t.Fatalf("delete manifest missing name or namespace:\n%s", text)
	}
}

func TestAPIsNamespaceIsSeparateFromControlPlaneNamespace(t *testing.T) {
	if APIsNamespace == "" || APIsNamespace == ManagedTag {
		t.Fatalf("APIs must use their own namespace, not %q", ManagedTag)
	}
}
