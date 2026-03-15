package getklogs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRenderOutputGoldenFiles(t *testing.T) {
	tests := []struct {
		name    string
		options Options
		golden  string
	}{
		{
			name:    "json",
			options: Options{Meta: true},
			golden:  "render_json.golden",
		},
		{
			name:    "yaml",
			options: Options{Output: OutputFormatYAML, Meta: true},
			golden:  "render_yaml.golden",
		},
		{
			name:    "raw",
			options: Options{Output: OutputFormatRaw, Meta: true},
			golden:  "render_raw.golden",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := renderOutput(renderGoldenEntries(), NormalizeOptions(tt.options))
			if err != nil {
				t.Fatalf("renderOutput returned error: %v", err)
			}

			expected, err := os.ReadFile(filepath.Join("testdata", tt.golden))
			if err != nil {
				t.Fatalf("ReadFile returned error: %v", err)
			}

			if string(content) != string(expected) {
				t.Fatalf("golden mismatch for %s\nexpected:\n%s\nactual:\n%s", tt.name, string(expected), string(content))
			}
		})
	}
}

func renderGoldenEntries() []LogEntry {
	return []LogEntry{
		{
			Timestamp:     "2026-03-14T10:00:00Z",
			PodName:       "frontend-a",
			ContainerName: "main",
			Line:          `2026-03-14T10:00:00Z {"level":"info","msg":"hello"}`,
			Message:       `{"level":"info","msg":"hello"}`,
		},
		{
			Timestamp:     "2026-03-14T10:00:01Z",
			PodName:       "frontend-b",
			ContainerName: "sidecar",
			Line:          "2026-03-14T10:00:01Z plain",
			Message:       "plain",
		},
	}
}
