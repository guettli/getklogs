package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guettli/getklogs/internal/getklogs"
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

func TestRootCommandWritesFilesToOutDir(t *testing.T) {
	tempDir := t.TempDir()
	useTestCluster(t, rootTestCluster{
		workloads: []getklogs.Workload{{
			Namespace: "team-a",
			Kind:      "Deployment",
			Name:      "frontend",
		}},
		targetsByTarget: map[string]getklogs.WorkloadTargets{
			"Deployment/team-a/frontend": {Containers: []getklogs.ContainerRef{{PodName: "frontend-a", ContainerName: "main"}}},
		},
		logs: map[string][]getklogs.LogEntry{
			"frontend-a/main": {{
				Timestamp:     "2026-03-14T10:00:00Z",
				PodName:       "frontend-a",
				ContainerName: "main",
				Line:          "2026-03-14T10:00:00Z hello",
				Message:       "hello",
			}},
		},
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := NewRootCmd(strings.NewReader(""), &stdout, &stderr)
	cmd.SetArgs([]string{"frontend", "--outdir", tempDir})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(tempDir, "frontend--team-a-*.log"))
	if err != nil {
		t.Fatalf("Glob returned error: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 output file, got %v", matches)
	}

	content, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if !strings.Contains(string(content), `"message":"hello"`) {
		t.Fatalf("unexpected file output: %q", string(content))
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected empty stdout, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Writing 1 logs to "+matches[0]) {
		t.Fatalf("expected stderr to mention written log count, got %q", stderr.String())
	}
}

func TestRootCommandWritesOneFilePerContainer(t *testing.T) {
	tempDir := t.TempDir()
	useTestCluster(t, rootTestCluster{
		workloads: []getklogs.Workload{{
			Namespace: "team-a",
			Kind:      "Deployment",
			Name:      "frontend",
		}},
		targetsByTarget: map[string]getklogs.WorkloadTargets{
			"Deployment/team-a/frontend": {Containers: []getklogs.ContainerRef{
				{PodName: "frontend-a", ContainerName: "main"},
				{PodName: "frontend-a", ContainerName: "sidecar"},
			}},
		},
		logs: map[string][]getklogs.LogEntry{
			"frontend-a/main": {{
				Timestamp:     "2026-03-14T10:00:00Z",
				PodName:       "frontend-a",
				ContainerName: "main",
				Line:          "2026-03-14T10:00:00Z hello",
				Message:       "hello",
			}},
			"frontend-a/sidecar": {{
				Timestamp:     "2026-03-14T10:00:01Z",
				PodName:       "frontend-a",
				ContainerName: "sidecar",
				Line:          "2026-03-14T10:00:01Z world",
				Message:       "world",
			}},
		},
	})

	var stderr bytes.Buffer
	cmd := NewRootCmd(strings.NewReader(""), &bytes.Buffer{}, &stderr)
	cmd.SetArgs([]string{"frontend", "--per-container", "--outdir", tempDir})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	mainMatches, err := filepath.Glob(filepath.Join(tempDir, "frontend--team-a--frontend-a--main-*.log"))
	if err != nil {
		t.Fatalf("Glob main returned error: %v", err)
	}
	sidecarMatches, err := filepath.Glob(filepath.Join(tempDir, "frontend--team-a--frontend-a--sidecar-*.log"))
	if err != nil {
		t.Fatalf("Glob sidecar returned error: %v", err)
	}
	if len(mainMatches) != 1 {
		t.Fatalf("expected 1 main container file, got %v", mainMatches)
	}
	if len(sidecarMatches) != 1 {
		t.Fatalf("expected 1 sidecar container file, got %v", sidecarMatches)
	}
}

func TestRootCommandWritesMultipleYAMLDocumentsToStdout(t *testing.T) {
	useTestCluster(t, rootTestCluster{
		workloads: []getklogs.Workload{
			{Namespace: "team-a", Kind: "Deployment", Name: "frontend"},
			{Namespace: "team-a", Kind: "StatefulSet", Name: "database"},
		},
		targetsByTarget: map[string]getklogs.WorkloadTargets{
			"Deployment/team-a/frontend":  {Containers: []getklogs.ContainerRef{{PodName: "frontend-a", ContainerName: "main"}}},
			"StatefulSet/team-a/database": {Containers: []getklogs.ContainerRef{{PodName: "database-0", ContainerName: "main"}}},
		},
		logs: map[string][]getklogs.LogEntry{
			"frontend-a/main": {{Timestamp: "2026-03-14T10:00:00Z", PodName: "frontend-a", ContainerName: "main", Line: "2026-03-14T10:00:00Z first", Message: "first"}},
			"database-0/main": {{Timestamp: "2026-03-14T10:00:01Z", PodName: "database-0", ContainerName: "main", Line: "2026-03-14T10:00:01Z second", Message: "second"}},
		},
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := NewRootCmd(strings.NewReader(""), &stdout, &stderr)
	cmd.SetArgs([]string{"--all", "--stdout", "--output", "yaml"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if strings.Count(stdout.String(), "---\n") != 1 {
		t.Fatalf("expected yaml document separator once, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "message: first") || !strings.Contains(stdout.String(), "message: second") {
		t.Fatalf("unexpected yaml stdout: %q", stdout.String())
	}
	if strings.Contains(stderr.String(), "Writing ") {
		t.Fatalf("did not expect file output, got %q", stderr.String())
	}
}

func TestRootCommandIncludesMetadataWithMetaFlag(t *testing.T) {
	useTestCluster(t, rootTestCluster{
		workloads: []getklogs.Workload{{
			Namespace: "team-a",
			Kind:      "Deployment",
			Name:      "frontend",
		}},
		targetsByTarget: map[string]getklogs.WorkloadTargets{
			"Deployment/team-a/frontend": {Containers: []getklogs.ContainerRef{{PodName: "frontend-a", ContainerName: "main"}}},
		},
		logs: map[string][]getklogs.LogEntry{
			"frontend-a/main": {{
				Timestamp:     "2026-03-14T10:00:00Z",
				PodName:       "frontend-a",
				ContainerName: "main",
				Line:          "2026-03-14T10:00:00Z hello",
				Message:       "hello",
			}},
		},
	})

	var stdout bytes.Buffer
	cmd := NewRootCmd(strings.NewReader(""), &stdout, &bytes.Buffer{})
	cmd.SetArgs([]string{"--meta", "--stdout", "frontend"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), `"source_container":"main"`) {
		t.Fatalf("expected source_container in stdout, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"source_pod":"frontend-a"`) {
		t.Fatalf("expected source_pod in stdout, got %q", stdout.String())
	}
}

func TestRootCommandRejectsStdoutWithOutDir(t *testing.T) {
	cmd := NewRootCmd(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{"--stdout", "--outdir", "logs"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Error() != "--outdir cannot be used with --stdout" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRootCommandRejectsPerContainerWithStdout(t *testing.T) {
	cmd := NewRootCmd(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{"--stdout", "--per-container"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Error() != "--per-container cannot be used with --stdout" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRootCommandPassesKubeconfigToClusterFactory(t *testing.T) {
	original := newCluster
	t.Cleanup(func() {
		newCluster = original
	})

	var gotKubeconfig string
	newCluster = func(kubeconfig string) (getklogs.ClusterAPI, error) {
		gotKubeconfig = kubeconfig
		return rootTestCluster{
			workloads: []getklogs.Workload{{
				Namespace: "team-a",
				Kind:      "Deployment",
				Name:      "frontend",
			}},
			targetsByTarget: map[string]getklogs.WorkloadTargets{
				"Deployment/team-a/frontend": {Containers: []getklogs.ContainerRef{{PodName: "frontend-a", ContainerName: "main"}}},
			},
			logs: map[string][]getklogs.LogEntry{
				"frontend-a/main": {{Timestamp: "2026-03-14T10:00:00Z", PodName: "frontend-a", ContainerName: "main", Line: "2026-03-14T10:00:00Z hello", Message: "hello"}},
			},
		}, nil
	}

	cmd := NewRootCmd(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{"--kubeconfig", "/tmp/custom-kubeconfig", "--stdout", "frontend"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if gotKubeconfig != "/tmp/custom-kubeconfig" {
		t.Fatalf("expected kubeconfig to be passed through, got %q", gotKubeconfig)
	}
}

func TestRootCommandHelpMentionsKubeconfigDefault(t *testing.T) {
	var stdout bytes.Buffer
	cmd := NewRootCmd(strings.NewReader(""), &stdout, &bytes.Buffer{})
	cmd.SetArgs([]string{"--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	helpText := stdout.String()
	if !strings.Contains(helpText, "Use --kubeconfig to set an explicit kubeconfig path.") {
		t.Fatalf("expected help text to mention explicit kubeconfig option, got %q", helpText)
	}
	if !strings.Contains(helpText, "KUBECONFIG environment variable") {
		t.Fatalf("expected help text to mention KUBECONFIG, got %q", helpText)
	}
	if !strings.Contains(helpText, "Only include pods on nodes matching this glob pattern") {
		t.Fatalf("expected help text to mention node filtering, got %q", helpText)
	}
	if !strings.Contains(helpText, "--meta") {
		t.Fatalf("expected help text to mention metadata flag, got %q", helpText)
	}
	if !strings.Contains(helpText, "--per-container") {
		t.Fatalf("expected help text to mention per-container flag, got %q", helpText)
	}
}

func useTestCluster(t *testing.T, cluster rootTestCluster) {
	t.Helper()

	original := newCluster
	newCluster = func(string) (getklogs.ClusterAPI, error) {
		return cluster, nil
	}
	t.Cleanup(func() {
		newCluster = original
	})
}

type rootTestCluster struct {
	workloads       []getklogs.Workload
	pods            []getklogs.Workload
	standalonePods  []getklogs.Workload
	targetsByTarget map[string]getklogs.WorkloadTargets
	logs            map[string][]getklogs.LogEntry
}

func (c rootTestCluster) ListWorkloads(context.Context, string, string) ([]getklogs.Workload, error) {
	return c.workloads, nil
}

func (c rootTestCluster) ListPods(context.Context, string, string) ([]getklogs.Workload, error) {
	return c.pods, nil
}

func (c rootTestCluster) ListStandalonePods(context.Context, string, string) ([]getklogs.Workload, error) {
	return c.standalonePods, nil
}

func (c rootTestCluster) ListContainersForWorkload(_ context.Context, workload getklogs.Workload, _ string) (getklogs.WorkloadTargets, error) {
	return c.targetsByTarget[rootTargetKey(workload)], nil
}

func (c rootTestCluster) ResolveWorkloadTargets(_ context.Context, workloads []getklogs.Workload, _ string) (map[string]getklogs.WorkloadTargets, error) {
	resolved := make(map[string]getklogs.WorkloadTargets, len(workloads))
	for _, workload := range workloads {
		resolved[rootTargetKey(workload)] = c.targetsByTarget[rootTargetKey(workload)]
	}
	return resolved, nil
}

func (c rootTestCluster) GetLogs(_ context.Context, _ string, podName, containerName string, _ time.Duration) ([]getklogs.LogEntry, error) {
	return c.logs[podName+"/"+containerName], nil
}

func rootTargetKey(workload getklogs.Workload) string {
	return workload.Kind + "/" + workload.Namespace + "/" + workload.Name
}
