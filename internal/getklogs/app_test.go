package getklogs

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNormalizeOptionsUsesLogSuffixAsOutputFile(t *testing.T) {
	options := NormalizeOptions(Options{
		Since:     15 * time.Minute,
		TermQuery: "api.log",
	})

	if options.OutputFile != "api.log" {
		t.Fatalf("expected output file api.log, got %q", options.OutputFile)
	}
	if options.TermQuery != "api" {
		t.Fatalf("expected term query api, got %q", options.TermQuery)
	}
	if options.Since != 15*time.Minute {
		t.Fatalf("expected since 15m, got %s", options.Since)
	}
}

func TestFilterWorkloadsMatchesNamespaceKindAndNameCaseInsensitive(t *testing.T) {
	workloads := []Workload{
		{Namespace: "alpha", Kind: "Deployment", Name: "frontend"},
		{Namespace: "beta", Kind: "StatefulSet", Name: "database"},
	}

	matches := FilterWorkloads(workloads, "BETA\tstate")

	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].Name != "database" {
		t.Fatalf("expected database, got %q", matches[0].Name)
	}
}

func TestSelectPodsForWorkloadFollowsReplicaSetOwnerToDeployment(t *testing.T) {
	controller := true
	pods := []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "frontend-abc",
				OwnerReferences: []metav1.OwnerReference{{
					Kind:       "ReplicaSet",
					Name:       "frontend-rs",
					Controller: &controller,
				}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "database-0",
				OwnerReferences: []metav1.OwnerReference{{
					Kind:       "StatefulSet",
					Name:       "database",
					Controller: &controller,
				}},
			},
		},
	}
	replicaSets := []appsv1.ReplicaSet{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "frontend-rs",
				OwnerReferences: []metav1.OwnerReference{{
					Kind:       "Deployment",
					Name:       "frontend",
					Controller: &controller,
				}},
			},
		},
	}

	selected := SelectPodsForWorkload(pods, replicaSets, Workload{
		Namespace: "default",
		Kind:      "Deployment",
		Name:      "frontend",
	})

	if len(selected) != 1 {
		t.Fatalf("expected 1 selected pod, got %d", len(selected))
	}
	if selected[0].Name != "frontend-abc" {
		t.Fatalf("expected frontend-abc, got %q", selected[0].Name)
	}
}

func TestReadLogEntriesKeepsOriginalLineAndTimestamp(t *testing.T) {
	reader := strings.NewReader("2026-03-14T10:00:00Z hello\n2026-03-14T10:00:01Z world\n")

	entries, err := readLogEntries(reader, "pod-a", "main")
	if err != nil {
		t.Fatalf("readLogEntries returned error: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Timestamp != "2026-03-14T10:00:00Z" {
		t.Fatalf("unexpected timestamp: %q", entries[0].Timestamp)
	}
	if entries[1].Line != "2026-03-14T10:00:01Z world" {
		t.Fatalf("unexpected line: %q", entries[1].Line)
	}
}

func TestAppRunSortsLogsByTimestampThenPodThenContainer(t *testing.T) {
	cluster := fakeCluster{
		workloads: []Workload{{
			Namespace: "team-a",
			Kind:      "Deployment",
			Name:      "frontend",
		}},
		containers: []ContainerRef{
			{PodName: "frontend-b", ContainerName: "sidecar"},
			{PodName: "frontend-a", ContainerName: "main"},
		},
		logs: map[string][]LogEntry{
			"frontend-b/sidecar": {{
				Timestamp:     "2026-03-14T10:00:01Z",
				PodName:       "frontend-b",
				ContainerName: "sidecar",
				Line:          "2026-03-14T10:00:01Z second",
			}},
			"frontend-a/main": {{
				Timestamp:     "2026-03-14T10:00:00Z",
				PodName:       "frontend-a",
				ContainerName: "main",
				Line:          "2026-03-14T10:00:00Z first",
			}},
		},
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := App{
		Cluster: cluster,
		Stdin:   strings.NewReader(""),
		Stdout:  &stdout,
		Stderr:  &stderr,
	}

	err := app.Run(context.Background(), Options{TermQuery: "frontend", Since: time.Hour})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	output := strings.TrimSpace(stdout.String())
	expected := "frontend-a main 2026-03-14T10:00:00Z first\nfrontend-b sidecar 2026-03-14T10:00:01Z second"
	if output != expected {
		t.Fatalf("unexpected output:\n%s", output)
	}
}

type fakeCluster struct {
	workloads  []Workload
	containers []ContainerRef
	logs       map[string][]LogEntry
}

func (f fakeCluster) ListWorkloads(context.Context, string) ([]Workload, error) {
	return f.workloads, nil
}

func (f fakeCluster) ListContainersForWorkload(context.Context, Workload) ([]ContainerRef, error) {
	return f.containers, nil
}

func (f fakeCluster) GetLogs(_ context.Context, _ string, podName, containerName string, _ time.Duration) ([]LogEntry, error) {
	return f.logs[podName+"/"+containerName], nil
}
