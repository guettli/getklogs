package getklogs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manifoldco/promptui"
	"go.yaml.in/yaml/v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNormalizeOptionsSetsDefaultOutputFormat(t *testing.T) {
	options := NormalizeOptions(Options{
		Since:     15 * time.Minute,
		TermQuery: "api",
	})

	if options.TermQuery != "api" {
		t.Fatalf("expected term query api, got %q", options.TermQuery)
	}
	if options.Since != 15*time.Minute {
		t.Fatalf("expected since 15m, got %s", options.Since)
	}
	if options.Output != OutputFormatJSON {
		t.Fatalf("expected default output format json, got %q", options.Output)
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

func TestChooseWorkloadInteractivelyFallsBackToNumericPromptWithoutTTY(t *testing.T) {
	workloads := []Workload{
		{Namespace: "team-a", Kind: "Deployment", Name: "frontend", Ready: 2, Desired: 2},
		{Namespace: "team-b", Kind: "StatefulSet", Name: "database", Ready: 1, Desired: 1},
	}

	var stdout bytes.Buffer
	selected, err := chooseWorkloadInteractively(strings.NewReader("2\n"), &stdout, workloads)
	if err != nil {
		t.Fatalf("chooseWorkloadInteractively returned error: %v", err)
	}

	if selected.Name != "database" {
		t.Fatalf("expected database, got %q", selected.Name)
	}
	if !strings.Contains(stdout.String(), "Enter number [1-2]: ") {
		t.Fatalf("expected numeric prompt in output, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "frontend | team-a | Deployment | 2/2") {
		t.Fatalf("expected separated workload label in output, got %q", stdout.String())
	}
}

func TestFormatWorkloadUsesVisibleSeparators(t *testing.T) {
	formatted := formatWorkload(Workload{
		Namespace: "mgt-system",
		Kind:      "Deployment",
		Name:      "cso-controller-manager",
		Ready:     2,
		Desired:   2,
	})

	expected := "cso-controller-manager | mgt-system | Deployment | 2/2"
	if formatted != expected {
		t.Fatalf("expected %q, got %q", expected, formatted)
	}
}

func TestBuildOutputFilenameUsesWorkloadAndUTCDate(t *testing.T) {
	filename := buildOutputFilename(Workload{
		Namespace: "kube-system",
		Name:      "ccm",
	}, time.Date(2026, 3, 14, 11, 22, 33, 0, time.FixedZone("CET", 3600)), OutputFormatJSON)

	expected := "ccm--kube-system-2026-03-14_10-22-33Z.log"
	if filename != expected {
		t.Fatalf("expected %q, got %q", expected, filename)
	}
}

func TestBuildOutputFilenameUsesYAMLExtension(t *testing.T) {
	filename := buildOutputFilename(Workload{
		Namespace: "kube-system",
		Name:      "ccm",
	}, time.Date(2026, 3, 14, 11, 22, 33, 0, time.UTC), OutputFormatYAML)

	expected := "ccm--kube-system-2026-03-14_11-22-33Z.yaml"
	if filename != expected {
		t.Fatalf("expected %q, got %q", expected, filename)
	}
}

func TestMatchWorkloadSearchMatchesAcrossDisplayedFields(t *testing.T) {
	workload := Workload{
		Namespace: "kube-system",
		Kind:      "Deployment",
		Name:      "ccm",
		Ready:     1,
		Desired:   1,
	}

	for _, input := range []string{"ccm", "kube", "deploy", "1/1"} {
		if !matchWorkloadSearch(input, workload) {
			t.Fatalf("expected input %q to match workload", input)
		}
	}
}

func TestChooseWorkloadWithPromptReturnsSelectedWorkload(t *testing.T) {
	original := runWorkloadPrompt
	t.Cleanup(func() {
		runWorkloadPrompt = original
	})

	runWorkloadPrompt = func(stdin io.ReadCloser, stdout io.WriteCloser, workloads []Workload) (int, string, error) {
		return 1, workloads[1].Name, nil
	}

	workloads := []Workload{
		{Namespace: "team-a", Kind: "Deployment", Name: "frontend"},
		{Namespace: "team-b", Kind: "StatefulSet", Name: "database"},
	}

	selected, err := chooseWorkloadWithPrompt(strings.NewReader(""), &bytes.Buffer{}, workloads)
	if err != nil {
		t.Fatalf("chooseWorkloadWithPrompt returned error: %v", err)
	}
	if selected.Name != "database" {
		t.Fatalf("expected database, got %q", selected.Name)
	}
}

func TestChooseWorkloadWithPromptMapsAbortError(t *testing.T) {
	original := runWorkloadPrompt
	t.Cleanup(func() {
		runWorkloadPrompt = original
	})

	runWorkloadPrompt = func(stdin io.ReadCloser, stdout io.WriteCloser, workloads []Workload) (int, string, error) {
		return 0, "", promptui.ErrInterrupt
	}

	_, err := chooseWorkloadWithPrompt(strings.NewReader(""), &bytes.Buffer{}, []Workload{{Namespace: "team-a", Kind: "Deployment", Name: "frontend"}})
	if err == nil {
		t.Fatal("expected an abort error")
	}
	if err.Error() != "workload selection aborted" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAppSelectWorkloadUsesCustomChooserWhenTermIsEmpty(t *testing.T) {
	expected := Workload{Namespace: "team-b", Kind: "StatefulSet", Name: "database"}
	app := App{
		ChooseWorkload: func(stdin io.Reader, stdout io.Writer, workloads []Workload) (Workload, error) {
			return expected, nil
		},
	}

	selected, err := app.selectWorkload([]Workload{
		{Namespace: "team-a", Kind: "Deployment", Name: "frontend"},
		expected,
	}, "")
	if err != nil {
		t.Fatalf("selectWorkload returned error: %v", err)
	}
	if selected != expected {
		t.Fatalf("expected %+v, got %+v", expected, selected)
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
	if entries[0].Message != "hello" {
		t.Fatalf("unexpected message: %q", entries[0].Message)
	}
}

func TestRenderEntriesDefaultsToJSONWithoutSource(t *testing.T) {
	lines, err := renderEntries([]LogEntry{{
		Timestamp:     "2026-03-14T10:00:00Z",
		PodName:       "frontend-a",
		ContainerName: "main",
		Line:          "2026-03-14T10:00:00Z hello",
		Message:       "hello",
	}}, Options{})
	if err != nil {
		t.Fatalf("renderEntries returned error: %v", err)
	}

	expected := `{"kubernetes_timestamp":"2026-03-14T10:00:00Z","message":"hello"}`
	if len(lines) != 1 || lines[0] != expected {
		t.Fatalf("unexpected lines: %#v", lines)
	}
}

func TestRenderEntriesMergesJSONObjectAndAddsSourceOnDemand(t *testing.T) {
	lines, err := renderEntries([]LogEntry{{
		Timestamp:     "2026-03-14T10:00:00Z",
		PodName:       "frontend-a",
		ContainerName: "main",
		Line:          `2026-03-14T10:00:00Z {"level":"info","msg":"hello"}`,
		Message:       `{"level":"info","msg":"hello"}`,
	}}, Options{AddSource: true})
	if err != nil {
		t.Fatalf("renderEntries returned error: %v", err)
	}

	expected := `{"kubernetes_timestamp":"2026-03-14T10:00:00Z","level":"info","msg":"hello","source_container":"main","source_pod":"frontend-a"}`
	if len(lines) != 1 || lines[0] != expected {
		t.Fatalf("unexpected lines: %#v", lines)
	}
}

func TestRenderEntriesKeepsOriginalLinesWithoutSourceWhenNoToJSON(t *testing.T) {
	lines, err := renderEntries([]LogEntry{{
		Timestamp:     "2026-03-14T10:00:00Z",
		PodName:       "frontend-a",
		ContainerName: "main",
		Line:          "2026-03-14T10:00:00Z hello",
		Message:       "hello",
	}}, Options{NoToJSON: true})
	if err != nil {
		t.Fatalf("renderEntries returned error: %v", err)
	}

	expected := "2026-03-14T10:00:00Z hello"
	if len(lines) != 1 || lines[0] != expected {
		t.Fatalf("unexpected lines: %#v", lines)
	}
}

func TestRenderEntriesAddsSourceOnlyWhenRequested(t *testing.T) {
	lines, err := renderEntries([]LogEntry{{
		Timestamp:     "2026-03-14T10:00:00Z",
		PodName:       "frontend-a",
		ContainerName: "main",
		Line:          "2026-03-14T10:00:00Z hello",
		Message:       "hello",
	}}, Options{NoToJSON: true, AddSource: true})
	if err != nil {
		t.Fatalf("renderEntries returned error: %v", err)
	}

	expected := "frontend-a main 2026-03-14T10:00:00Z hello"
	if len(lines) != 1 || lines[0] != expected {
		t.Fatalf("unexpected lines: %#v", lines)
	}
}

func TestRenderEntriesParsesKlogStyleMessages(t *testing.T) {
	lines, err := renderEntries([]LogEntry{{
		Timestamp:     "2026-03-14T10:36:31.700000Z",
		PodName:       "frontend-a",
		ContainerName: "main",
		Line:          `2026-03-14T10:36:31.700000Z E0314 10:36:31.625751       1 controller.go:347] "Reconciler error" err="failed to get Cluster/databases-test: Cluster.cluster.x-k8s.io \"databases-test\" not found" controller="topology/machineset" controllerGroup="cluster.x-k8s.io" controllerKind="MachineSet" MachineSet="org-testing/databases-test-md-bm-q2gr4-5vldw" namespace="org-testing" name="databases-test-md-bm-q2gr4-5vldw" reconcileID="8960b81f-579c-433b-8cba-4fefc004c51d"`,
		Message:       `E0314 10:36:31.625751       1 controller.go:347] "Reconciler error" err="failed to get Cluster/databases-test: Cluster.cluster.x-k8s.io \"databases-test\" not found" controller="topology/machineset" controllerGroup="cluster.x-k8s.io" controllerKind="MachineSet" MachineSet="org-testing/databases-test-md-bm-q2gr4-5vldw" namespace="org-testing" name="databases-test-md-bm-q2gr4-5vldw" reconcileID="8960b81f-579c-433b-8cba-4fefc004c51d"`,
	}}, Options{})
	if err != nil {
		t.Fatalf("renderEntries returned error: %v", err)
	}

	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &payload); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}

	assertEqual := func(key string, expected any) {
		t.Helper()
		if payload[key] != expected {
			t.Fatalf("expected %s=%v, got %#v", key, expected, payload[key])
		}
	}

	assertEqual("kubernetes_timestamp", "2026-03-14T10:36:31.700000Z")
	assertEqual("level", "E0314")
	assertEqual("log_timestamp", "2026-03-14T10:36:31.625751Z")
	assertEqual("thread_id", float64(1))
	assertEqual("caller", "controller.go:347")
	assertEqual("message", "Reconciler error")
	assertEqual("controller", "topology/machineset")
	assertEqual("controllerGroup", "cluster.x-k8s.io")
	assertEqual("controllerKind", "MachineSet")
	assertEqual("MachineSet", "org-testing/databases-test-md-bm-q2gr4-5vldw")
	assertEqual("namespace", "org-testing")
	assertEqual("name", "databases-test-md-bm-q2gr4-5vldw")
	assertEqual("reconcileID", "8960b81f-579c-433b-8cba-4fefc004c51d")
	assertEqual("err", `failed to get Cluster/databases-test: Cluster.cluster.x-k8s.io "databases-test" not found`)
}

func TestRenderEntriesParsesRawKlogLinesWithoutOuterTimestamp(t *testing.T) {
	lines, err := renderEntries([]LogEntry{{
		Line:    `I0314 11:11:53.564201       1 client.go:214] "Connect to server" serverID="f154e1fe-358c-49c2-88cf-ec36ad33b39e"`,
		Message: `I0314 11:11:53.564201       1 client.go:214] "Connect to server" serverID="f154e1fe-358c-49c2-88cf-ec36ad33b39e"`,
	}}, Options{})
	if err != nil {
		t.Fatalf("renderEntries returned error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &payload); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}

	if payload["level"] != "I0314" || payload["log_timestamp"] != "03-14T11:11:53.564201" || payload["message"] != "Connect to server" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestRenderEntriesParsesKlogLinesWithPlainTextMessage(t *testing.T) {
	lines, err := renderEntries([]LogEntry{{
		Line:    `W0314 11:18:26.850028       1 reflector.go:362] The watchlist request ended with an error, falling back to the standard LIST/WATCH semantics because making progress is better than deadlocking, err = the server could not find the requested resource`,
		Message: `W0314 11:18:26.850028       1 reflector.go:362] The watchlist request ended with an error, falling back to the standard LIST/WATCH semantics because making progress is better than deadlocking, err = the server could not find the requested resource`,
	}}, Options{})
	if err != nil {
		t.Fatalf("renderEntries returned error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &payload); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}

	if payload["level"] != "W0314" || payload["caller"] != "reflector.go:362" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if !strings.Contains(payload["message"].(string), "falling back to the standard LIST/WATCH semantics") {
		t.Fatalf("unexpected message payload: %#v", payload)
	}
}

func TestRenderEntriesParsesLogfmtLines(t *testing.T) {
	lines, err := renderEntries([]LogEntry{{
		Line:    `time="2026-03-14T11:11:47Z" level=info msg="Refreshing app status" application=argocd/argocd`,
		Message: `time="2026-03-14T11:11:47Z" level=info msg="Refreshing app status" application=argocd/argocd`,
	}}, Options{})
	if err != nil {
		t.Fatalf("renderEntries returned error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &payload); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}

	if payload["log_timestamp"] != "2026-03-14T11:11:47Z" || payload["level"] != "info" || payload["message"] != "Refreshing app status" || payload["application"] != "argocd/argocd" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestRenderEntriesParsesAccessLogLines(t *testing.T) {
	lines, err := renderEntries([]LogEntry{{
		Line:    `192.168.3.113 - - [14/Mar/2026:11:11:52 +0000] "GET / HTTP/1.1" 200 702 "-" "kube-probe/1.32" "-"`,
		Message: `192.168.3.113 - - [14/Mar/2026:11:11:52 +0000] "GET / HTTP/1.1" 200 702 "-" "kube-probe/1.32" "-"`,
	}}, Options{})
	if err != nil {
		t.Fatalf("renderEntries returned error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &payload); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}

	if payload["remote_addr"] != "192.168.3.113" || payload["method"] != "GET" || payload["request_uri"] != "/" || payload["status"] != float64(200) {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestRenderEntriesParsesSquidLines(t *testing.T) {
	lines, err := renderEntries([]LogEntry{{
		Line:    `2026/03/14 11:16:51| Logfile: opening log stdio:/var/log/squid/netdb.state`,
		Message: `2026/03/14 11:16:51| Logfile: opening log stdio:/var/log/squid/netdb.state`,
	}}, Options{})
	if err != nil {
		t.Fatalf("renderEntries returned error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &payload); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}

	if payload["log_timestamp"] != "2026/03/14 11:16:51" || payload["message"] != "Logfile: opening log stdio:/var/log/squid/netdb.state" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestRenderOutputSupportsYAML(t *testing.T) {
	content, err := renderOutput([]LogEntry{{
		Timestamp:     "2026-03-14T10:00:00Z",
		PodName:       "frontend-a",
		ContainerName: "main",
		Line:          "2026-03-14T10:00:00Z hello",
		Message:       "hello",
	}}, Options{Output: OutputFormatYAML})
	if err != nil {
		t.Fatalf("renderOutput returned error: %v", err)
	}

	var payload []map[string]any
	if err := yaml.Unmarshal(content, &payload); err != nil {
		t.Fatalf("yaml.Unmarshal returned error: %v", err)
	}

	if len(payload) != 1 {
		t.Fatalf("expected 1 yaml item, got %d", len(payload))
	}
	if payload[0]["kubernetes_timestamp"] != "2026-03-14T10:00:00Z" || payload[0]["message"] != "hello" {
		t.Fatalf("unexpected yaml payload: %#v", payload)
	}
}

func TestSamplePodLogsRenderToJSON(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "pod-*.log"))
	if err != nil {
		t.Fatalf("Glob returned error: %v", err)
	}
	if len(files) == 0 {
		t.Skip("no sample pod logs found")
	}

	for _, path := range files {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			file, err := os.Open(path)
			if err != nil {
				t.Fatalf("Open returned error: %v", err)
			}
			defer func() {
				_ = file.Close()
			}()

			scanner := bufio.NewScanner(file)
			scanner.Buffer(make([]byte, 1024), 1024*1024*8)
			lineNumber := 0
			for scanner.Scan() {
				lineNumber++
				line := scanner.Text()
				if strings.TrimSpace(line) == "" {
					continue
				}

				rendered, err := renderEntry(LogEntry{
					Line:    line,
					Message: line,
				}, Options{})
				if err != nil {
					t.Fatalf("renderEntry failed at line %d: %v", lineNumber, err)
				}

				var payload map[string]any
				if err := json.Unmarshal([]byte(rendered), &payload); err != nil {
					t.Fatalf("invalid json at line %d: %v", lineNumber, err)
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatalf("Scanner returned error: %v", err)
			}
		})
	}
}

func TestAppRunWritesJSONLinesByDefault(t *testing.T) {
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
	tempDir := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd returned error: %v", err)
	}
	t.Cleanup(func() {
		if chdirErr := os.Chdir(oldWd); chdirErr != nil {
			t.Fatalf("failed to restore working directory: %v", chdirErr)
		}
	})
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir returned error: %v", err)
	}

	app := App{
		Cluster: cluster,
		Stdin:   strings.NewReader(""),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Now: func() time.Time {
			return time.Date(2026, 3, 14, 10, 20, 30, 0, time.UTC)
		},
	}

	err = app.Run(context.Background(), Options{TermQuery: "frontend", Since: time.Hour})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if stdout.Len() != 0 {
		t.Fatalf("expected stdout to stay empty, got %q", stdout.String())
	}

	filename := filepath.Join(tempDir, "frontend--team-a-2026-03-14_10-20-30Z.log")
	content, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}

	expected := strings.Join([]string{
		`{"kubernetes_timestamp":"2026-03-14T10:00:00Z","message":"first"}`,
		`{"kubernetes_timestamp":"2026-03-14T10:00:01Z","message":"second"}`,
	}, "\n") + "\n"
	if string(content) != expected {
		t.Fatalf("unexpected file output:\n%s", string(content))
	}

	if !strings.Contains(stderr.String(), "Writing logs to frontend--team-a-2026-03-14_10-20-30Z.log") {
		t.Fatalf("expected stderr to mention output file, got %q", stderr.String())
	}
}

func TestAppRunWritesYAMLWhenRequested(t *testing.T) {
	cluster := fakeCluster{
		workloads: []Workload{{
			Namespace: "team-a",
			Kind:      "Deployment",
			Name:      "frontend",
		}},
		containers: []ContainerRef{
			{PodName: "frontend-a", ContainerName: "main"},
		},
		logs: map[string][]LogEntry{
			"frontend-a/main": {{
				Timestamp:     "2026-03-14T10:00:00Z",
				PodName:       "frontend-a",
				ContainerName: "main",
				Line:          "2026-03-14T10:00:00Z first",
				Message:       "first",
			}},
		},
	}

	var stderr bytes.Buffer
	tempDir := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd returned error: %v", err)
	}
	t.Cleanup(func() {
		if chdirErr := os.Chdir(oldWd); chdirErr != nil {
			t.Fatalf("failed to restore working directory: %v", chdirErr)
		}
	})
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir returned error: %v", err)
	}

	app := App{
		Cluster: cluster,
		Stdin:   strings.NewReader(""),
		Stdout:  &bytes.Buffer{},
		Stderr:  &stderr,
		Now: func() time.Time {
			return time.Date(2026, 3, 14, 10, 20, 30, 0, time.UTC)
		},
	}

	err = app.Run(context.Background(), Options{TermQuery: "frontend", Since: time.Hour, Output: OutputFormatYAML})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	filename := filepath.Join(tempDir, "frontend--team-a-2026-03-14_10-20-30Z.yaml")
	content, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}

	var payload []map[string]any
	if err := yaml.Unmarshal(content, &payload); err != nil {
		t.Fatalf("yaml.Unmarshal returned error: %v", err)
	}
	if len(payload) != 1 || payload[0]["message"] != "first" {
		t.Fatalf("unexpected yaml payload: %#v", payload)
	}

	if !strings.Contains(stderr.String(), "Writing logs to frontend--team-a-2026-03-14_10-20-30Z.yaml") {
		t.Fatalf("expected stderr to mention yaml output file, got %q", stderr.String())
	}
}

func TestAppRunAddsSourceWhenRequested(t *testing.T) {
	cluster := fakeCluster{
		workloads: []Workload{{
			Namespace: "team-a",
			Kind:      "Deployment",
			Name:      "frontend",
		}},
		containers: []ContainerRef{
			{PodName: "frontend-a", ContainerName: "main"},
		},
		logs: map[string][]LogEntry{
			"frontend-a/main": {{
				Timestamp:     "2026-03-14T10:00:00Z",
				PodName:       "frontend-a",
				ContainerName: "main",
				Line:          "2026-03-14T10:00:00Z first",
				Message:       "first",
			}},
		},
	}

	var stderr bytes.Buffer
	tempDir := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd returned error: %v", err)
	}
	t.Cleanup(func() {
		if chdirErr := os.Chdir(oldWd); chdirErr != nil {
			t.Fatalf("failed to restore working directory: %v", chdirErr)
		}
	})
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir returned error: %v", err)
	}

	app := App{
		Cluster: cluster,
		Stdin:   strings.NewReader(""),
		Stdout:  &bytes.Buffer{},
		Stderr:  &stderr,
		Now: func() time.Time {
			return time.Date(2026, 3, 14, 10, 20, 30, 0, time.UTC)
		},
	}

	err = app.Run(context.Background(), Options{TermQuery: "frontend", Since: time.Hour, AddSource: true})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tempDir, "frontend--team-a-2026-03-14_10-20-30Z.log"))
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}

	expected := `{"kubernetes_timestamp":"2026-03-14T10:00:00Z","message":"first","source_container":"main","source_pod":"frontend-a"}` + "\n"
	if string(content) != expected {
		t.Fatalf("unexpected file output:\n%s", string(content))
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
