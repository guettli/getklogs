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

func TestRootCommandRejectsInvalidOutputBeforeTalkingToKubernetes(t *testing.T) {
	t.Setenv("KUBECONFIG", t.TempDir()+"/missing")

	cmd := NewRootCmd(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{"--output", "xml"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Error() != `unsupported output format "xml"` {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestToJSONCommandSupportsLongInputLines(t *testing.T) {
	t.Setenv("KUBECONFIG", t.TempDir()+"/missing")

	message := strings.Repeat("x", 128*1024)
	var stdout bytes.Buffer
	cmd := NewRootCmd(strings.NewReader("2026-03-14T10:00:00Z "+message+"\n"), &stdout, &bytes.Buffer{})
	cmd.SetArgs([]string{"tojson"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), `"message":"`+message+`"`) {
		t.Fatalf("expected long message in stdout, got %d bytes", stdout.Len())
	}
}
