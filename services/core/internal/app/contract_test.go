package app

import (
	"os"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/auth"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/media"
	"github.com/ardeshirazimi2000-design/urban-crisis-management-/services/core/internal/platform/config"
)

// TestOpenAPICoversRoutes fails CI when the implementation and contracts/openapi drift apart.
func TestOpenAPICoversRoutes(t *testing.T) {
	raw, err := os.ReadFile("../../../../contracts/openapi/core-api.v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("invalid OpenAPI YAML: %v", err)
	}
	documented := map[string]bool{}
	for path, ops := range spec.Paths {
		for method := range ops {
			documented[strings.ToUpper(method)+" "+path] = true
		}
	}
	cfg := config.Config{AppEnv: "local", AuthMode: "dev", DevJWTSecret: strings.Repeat("x", 32)}
	a := New(cfg, nil, auth.DevVerifier{}, &media.FSStore{})
	implemented := map[string]bool{}
	for _, r := range a.Routes {
		implemented[r] = true
	}
	var missing, extra []string
	for r := range implemented {
		if !documented[r] {
			missing = append(missing, r)
		}
	}
	for r := range documented {
		if !implemented[r] {
			extra = append(extra, r)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("routes not in OpenAPI: %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("OpenAPI paths not implemented: %v", extra)
	}
}
