package outbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEventSchemasExist ensures every event type emitted by the code has a versioned JSON Schema in contracts/events.
func TestEventSchemasExist(t *testing.T) {
	root := "../.."
	re := regexp.MustCompile(`Type:\s+"([a-z_]+\.[a-z_]+)"`)
	emitted := map[string]bool{"report.scored": true} // produced by ai-assist
	filepath.WalkDir(filepath.Join(root, "internal"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		b, _ := os.ReadFile(p)
		for _, m := range re.FindAllSubmatch(b, -1) {
			emitted[string(m[1])] = true
		}
		return nil
	})
	if len(emitted) < 10 {
		t.Fatalf("suspiciously few events found: %v", emitted)
	}
	dir := "../../../../contracts/events"
	for e := range emitted {
		b, err := os.ReadFile(filepath.Join(dir, e+".v1.json"))
		if err != nil {
			t.Errorf("missing schema for %s", e)
			continue
		}
		var s map[string]any
		if err := json.Unmarshal(b, &s); err != nil {
			t.Errorf("invalid schema %s: %v", e, err)
		}
	}
}
