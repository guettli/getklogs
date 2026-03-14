package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestToJSONCommandConvertsStdinWithoutKubernetes(t *testing.T) {
	t.Setenv("KUBECONFIG", t.TempDir()+"/missing")

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	cmd := NewRootCmd(strings.NewReader("2026-03-14T10:00:00Z hello\n"), &stdout, &stderr)
	cmd.SetArgs([]string{"tojson"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	expected := "{\"kubernetes_timestamp\":\"2026-03-14T10:00:00Z\",\"message\":\"hello\"}\n"
	if stdout.String() != expected {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}
}
