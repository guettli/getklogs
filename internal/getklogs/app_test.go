package getklogs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	if options.OutDir != "." {
		t.Fatalf("expected default outdir '.', got %q", options.OutDir)
	}
}

func TestValidateOptionsRejectsUnsupportedOutputFormat(t *testing.T) {
	err := ValidateOptions(Options{Output: "xml"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Error() != `unsupported output format "xml"` {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateOptionsRejectsOutDirWithStdout(t *testing.T) {
	err := ValidateOptions(Options{Stdout: true, OutDir: "logs"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Error() != "--outdir cannot be used with --stdout" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateOptionsRejectsPerContainerWithStdout(t *testing.T) {
	err := ValidateOptions(Options{Stdout: true, PerContainer: true})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Error() != "--per-container cannot be used with --stdout" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNormalizeOptionsMakesFollowImplyStdout(t *testing.T) {
	options := NormalizeOptions(Options{Follow: true})
	if !options.Stdout {
		t.Fatal("expected follow to imply stdout")
	}
}

func TestValidateOptionsRejectsFollowWithTail(t *testing.T) {
	err := ValidateOptions(Options{Follow: true, Stdout: true, TailLines: 1})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Error() != "--follow cannot be used with --tail" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateOptionsRejectsInvalidNodePattern(t *testing.T) {
	err := ValidateOptions(Options{Node: "["})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Error() != `invalid --node pattern "[": syntax error in pattern` {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDescribeSinceWindowFormatsRelativeDuration(t *testing.T) {
	if got := DescribeSinceWindow(3 * time.Hour); got != "last 3h" {
		t.Fatalf("expected last 3h, got %q", got)
	}
}

func TestDescribeSinceWindowReturnsAllLogsWhenDisabled(t *testing.T) {
	if got := DescribeSinceWindow(0); got != "all available logs" {
		t.Fatalf("expected all available logs, got %q", got)
	}
}

func TestNoTargetsFoundErrorMentionsStandalonePodsForAll(t *testing.T) {
	err := noTargetsFoundError(Options{All: true, Namespace: "team-a"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Error() != `no Deployment/DaemonSet/StatefulSet or standalone pod found in namespace "team-a"` {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFilterWorkloadsMatchesNamespaceKindAndNameCaseInsensitive(t *testing.T) {
	workloads := []Workload{
		{Namespace: "alpha", Kind: "Deployment", Name: "frontend"},
		{Namespace: "beta", Kind: "StatefulSet", Name: "database"},
	}

	matches := FilterWorkloads(workloads, "  BETA   state  ")

	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].Name != "database" {
		t.Fatalf("expected database, got %q", matches[0].Name)
	}
}

func TestFilterWorkloadsMatchesOrderedTermsWithWildcardGap(t *testing.T) {
	workloads := []Workload{
		{Namespace: "team-a", Kind: "Deployment", Name: "foo-service-bar"},
		{Namespace: "team-a", Kind: "Deployment", Name: "bar-service-foo"},
	}

	matches := FilterWorkloads(workloads, "foo bar")

	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if matches[0].Name != "foo-service-bar" {
		t.Fatalf("expected foo-service-bar, got %q", matches[0].Name)
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

func TestChooseWorkloadByNumberAcceptsEOFWithoutTrailingNewline(t *testing.T) {
	workloads := []Workload{
		{Namespace: "team-a", Kind: "Deployment", Name: "frontend", Ready: 2, Desired: 2},
		{Namespace: "team-b", Kind: "StatefulSet", Name: "database", Ready: 1, Desired: 1},
	}

	var stdout bytes.Buffer
	selected, err := chooseWorkloadByNumber(strings.NewReader("2"), &stdout, workloads)
	if err != nil {
		t.Fatalf("chooseWorkloadByNumber returned error: %v", err)
	}
	if selected.Name != "database" {
		t.Fatalf("expected database, got %q", selected.Name)
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

func TestBuildOutputFilenameForContainerIncludesPodAndContainer(t *testing.T) {
	filename := buildOutputFilenameForContainer(
		Workload{Namespace: "kube-system", Name: "ccm"},
		ContainerRef{PodName: "ccm-a", ContainerName: "manager"},
		time.Date(2026, 3, 14, 11, 22, 33, 0, time.UTC),
		OutputFormatJSON,
	)

	expected := "ccm--kube-system--ccm-a--manager-2026-03-14_11-22-33Z.log"
	if filename != expected {
		t.Fatalf("expected %q, got %q", expected, filename)
	}
}

func TestFormatContainerRefsUsesPodAndContainerNames(t *testing.T) {
	formatted := formatContainerRefs([]ContainerRef{
		{PodName: "frontend-a", ContainerName: "main"},
		{PodName: "frontend-a", ContainerName: "sidecar"},
		{PodName: "frontend-b", ContainerName: "main"},
	})

	expected := "frontend-a/main frontend-a/sidecar frontend-b/main"
	if formatted != expected {
		t.Fatalf("expected %q, got %q", expected, formatted)
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

func TestAppChooseWorkloadUsesCustomChooser(t *testing.T) {
	expected := Workload{Namespace: "team-b", Kind: "StatefulSet", Name: "database"}
	app := App{
		ChooseWorkload: func(stdin io.Reader, stdout io.Writer, workloads []Workload) (Workload, error) {
			return expected, nil
		},
	}

	selected, err := app.chooseWorkload([]Workload{
		{Namespace: "team-a", Kind: "Deployment", Name: "frontend"},
		expected,
	})
	if err != nil {
		t.Fatalf("chooseWorkload returned error: %v", err)
	}
	if selected != expected {
		t.Fatalf("expected %+v, got %+v", expected, selected)
	}
}

func TestAppSelectTargetsUsesChooserForMultipleTermMatches(t *testing.T) {
	expected := Workload{Namespace: "team-b", Kind: "StatefulSet", Name: "database"}
	app := App{
		ChooseWorkload: func(stdin io.Reader, stdout io.Writer, workloads []Workload) (Workload, error) {
			if len(workloads) != 2 {
				t.Fatalf("expected 2 chooser workloads, got %d", len(workloads))
			}
			return expected, nil
		},
	}

	selected, err := app.selectTargets([]Workload{
		{Namespace: "team-a", Kind: "Deployment", Name: "database-api"},
		expected,
	}, Options{TermQuery: "data"})
	if err != nil {
		t.Fatalf("selectTargets returned error: %v", err)
	}
	if len(selected) != 1 || selected[0] != expected {
		t.Fatalf("expected %+v, got %+v", expected, selected)
	}
}

func TestAppListTargetsIncludesStandalonePodsWhenAllIsSet(t *testing.T) {
	app := App{
		Cluster: fakeCluster{
			workloads: []Workload{
				{Namespace: "team-a", Kind: "Deployment", Name: "frontend"},
			},
			standalonePods: []Workload{
				{Namespace: "kube-system", Kind: "Pod", Name: "kube-apiserver-node-1"},
			},
		},
	}

	targets, err := app.listTargets(context.Background(), Options{All: true, TermQuery: "api"})
	if err != nil {
		t.Fatalf("listTargets returned error: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(targets))
	}
	if targets[1].Kind != "Pod" || targets[1].Name != "kube-apiserver-node-1" {
		t.Fatalf("unexpected standalone pod target: %+v", targets[1])
	}
}

func TestAppListTargetsPassesNodeFilterToCluster(t *testing.T) {
	cluster := &nodeTrackingCluster{
		workloads: []Workload{
			{Namespace: "team-a", Kind: "Deployment", Name: "frontend"},
		},
	}
	app := App{Cluster: cluster}

	targets, err := app.listTargets(context.Background(), Options{Node: "*worker*"})
	if err != nil {
		t.Fatalf("listTargets returned error: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	if cluster.listWorkloadsNode != "*worker*" {
		t.Fatalf("expected node filter to reach ListWorkloads, got %q", cluster.listWorkloadsNode)
	}
}

func TestSelectPodsForWorkloadFollowsReplicaSetOwnerToDeployment(t *testing.T) {
	controller := true
	pods := []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "frontend-abc",
				Namespace: "default",
				OwnerReferences: []metav1.OwnerReference{{
					Kind:       "ReplicaSet",
					Name:       "frontend-rs",
					Controller: &controller,
				}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "database-0",
				Namespace: "default",
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
				Name:      "frontend-rs",
				Namespace: "default",
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

func TestFilterPodsByNodeSupportsExactAndGlobMatch(t *testing.T) {
	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "frontend-a"}, Spec: corev1.PodSpec{NodeName: "worker-a"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "frontend-b"}, Spec: corev1.PodSpec{NodeName: "worker-b"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "database-0"}, Spec: corev1.PodSpec{NodeName: "infra-1"}},
	}

	exact := filterPodsByNode(pods, "worker-a")
	if len(exact) != 1 || exact[0].Name != "frontend-a" {
		t.Fatalf("expected exact node match for frontend-a, got %+v", exact)
	}

	glob := filterPodsByNode(pods, "*worker*")
	if len(glob) != 2 {
		t.Fatalf("expected 2 glob matches, got %d", len(glob))
	}
	if glob[0].Name != "frontend-a" || glob[1].Name != "frontend-b" {
		t.Fatalf("unexpected glob matches: %+v", glob)
	}
}

func TestRolloutWarningForDeploymentWithMultipleReplicaSets(t *testing.T) {
	controller := true
	pods := []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "frontend-old",
				OwnerReferences: []metav1.OwnerReference{{
					Kind:       "ReplicaSet",
					Name:       "frontend-rs-old",
					Controller: &controller,
				}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "frontend-new",
				OwnerReferences: []metav1.OwnerReference{{
					Kind:       "ReplicaSet",
					Name:       "frontend-rs-new",
					Controller: &controller,
				}},
			},
		},
	}

	warning := rolloutWarningForWorkload(pods, Workload{Kind: "Deployment", Name: "frontend"})
	if !strings.Contains(warning, "multiple ReplicaSets") {
		t.Fatalf("expected deployment rollout warning, got %q", warning)
	}
	if !strings.Contains(warning, "frontend-rs-new") || !strings.Contains(warning, "frontend-rs-old") {
		t.Fatalf("expected ReplicaSet names in warning, got %q", warning)
	}
}

func TestRolloutWarningForStatefulSetWithMultipleRevisions(t *testing.T) {
	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "db-0", Labels: map[string]string{"controller-revision-hash": "db-a"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "db-1", Labels: map[string]string{"controller-revision-hash": "db-b"}}},
	}

	warning := rolloutWarningForWorkload(pods, Workload{Kind: "StatefulSet", Name: "db"})
	if !strings.Contains(warning, "multiple revisions") {
		t.Fatalf("expected statefulset rollout warning, got %q", warning)
	}
	if !strings.Contains(warning, "db-a") || !strings.Contains(warning, "db-b") {
		t.Fatalf("expected revision hashes in warning, got %q", warning)
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

func TestConvertInputParsesTimestampedAndRawLines(t *testing.T) {
	input := strings.NewReader("2026-03-14T10:00:00Z hello\nI0314 11:11:53.564201       1 client.go:214] \"Connect to server\" serverID=\"f154e1fe-358c-49c2-88cf-ec36ad33b39e\"\n")
	var output bytes.Buffer

	if err := ConvertInput(input, &output, Options{}); err != nil {
		t.Fatalf("ConvertInput returned error: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("json.Unmarshal first line returned error: %v", err)
	}
	if first["kubernetes_timestamp"] != "2026-03-14T10:00:00Z" || first["message"] != "hello" {
		t.Fatalf("unexpected first payload: %#v", first)
	}

	var second map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("json.Unmarshal second line returned error: %v", err)
	}
	if second["level"] != "INFO" || second["message"] != "Connect to server" {
		t.Fatalf("unexpected second payload: %#v", second)
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
	}}, Options{Meta: true})
	if err != nil {
		t.Fatalf("renderEntries returned error: %v", err)
	}

	expected := `{"kubernetes_timestamp":"2026-03-14T10:00:00Z","level":"info","msg":"hello","source_container":"main","source_pod":"frontend-a"}`
	if len(lines) != 1 || lines[0] != expected {
		t.Fatalf("unexpected lines: %#v", lines)
	}
}

func TestRenderEntriesKeepsOriginalLinesForRawOutput(t *testing.T) {
	lines, err := renderEntries([]LogEntry{{
		Timestamp:     "2026-03-14T10:00:00Z",
		PodName:       "frontend-a",
		ContainerName: "main",
		Line:          "2026-03-14T10:00:00Z hello",
		Message:       "hello",
	}}, Options{Output: OutputFormatRaw})
	if err != nil {
		t.Fatalf("renderEntries returned error: %v", err)
	}

	expected := "2026-03-14T10:00:00Z hello"
	if len(lines) != 1 || lines[0] != expected {
		t.Fatalf("unexpected lines: %#v", lines)
	}
}

func TestRenderEntriesRawDoesNotPrependTimestampWithoutOriginalLine(t *testing.T) {
	lines, err := renderEntries([]LogEntry{{
		Timestamp:     "2026-03-14T10:00:00Z",
		PodName:       "frontend-a",
		ContainerName: "main",
		Message:       "hello",
	}}, Options{Output: OutputFormatRaw})
	if err != nil {
		t.Fatalf("renderEntries returned error: %v", err)
	}

	expected := "hello"
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
	}}, Options{Output: OutputFormatRaw, Meta: true})
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
	assertEqual("level", "ERROR")
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

	if payload["level"] != "INFO" || payload["log_timestamp"] != "03-14T11:11:53.564201" || payload["message"] != "Connect to server" {
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

	if payload["level"] != "WARN" || payload["caller"] != "reflector.go:362" {
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

func TestRenderOutputSupportsRaw(t *testing.T) {
	content, err := renderOutput([]LogEntry{{
		Timestamp:     "2026-03-14T10:00:00Z",
		PodName:       "frontend-a",
		ContainerName: "main",
		Line:          "2026-03-14T10:00:00Z hello",
		Message:       "hello",
	}}, Options{Output: OutputFormatRaw})
	if err != nil {
		t.Fatalf("renderOutput returned error: %v", err)
	}

	if string(content) != "2026-03-14T10:00:00Z hello\n" {
		t.Fatalf("unexpected raw output: %q", string(content))
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
		targets: WorkloadTargets{Containers: []ContainerRef{
			{PodName: "frontend-b", ContainerName: "sidecar"},
			{PodName: "frontend-a", ContainerName: "main"},
		}},
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

	app := App{
		Cluster: cluster,
		Stdin:   strings.NewReader(""),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Now: func() time.Time {
			return time.Date(2026, 3, 14, 10, 20, 30, 0, time.UTC)
		},
	}

	err := app.Run(context.Background(), Options{TermQuery: "frontend", Since: time.Hour, OutDir: tempDir})
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

	if !strings.Contains(stderr.String(), "Containers: frontend-a/main frontend-b/sidecar\n") {
		t.Fatalf("expected stderr to contain container list, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Log range: last 1h\n") {
		t.Fatalf("expected stderr to mention log range, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Writing 2 logs to "+filename) {
		t.Fatalf("expected stderr to mention output file, got %q", stderr.String())
	}
	if !strings.HasSuffix(stderr.String(), "\n\n") {
		t.Fatalf("expected stderr to end with a blank line, got %q", stderr.String())
	}
}

func TestAppRunSkipsFileCreationWhenNoLogsAreCollected(t *testing.T) {
	cluster := fakeCluster{
		workloads: []Workload{{
			Namespace: "team-a",
			Kind:      "Deployment",
			Name:      "frontend",
		}},
		targets: WorkloadTargets{Containers: []ContainerRef{
			{PodName: "frontend-a", ContainerName: "main"},
		}},
		logs: map[string][]LogEntry{
			"frontend-a/main": nil,
		},
	}

	var stderr bytes.Buffer
	tempDir := t.TempDir()

	app := App{
		Cluster: cluster,
		Stdin:   strings.NewReader(""),
		Stdout:  &bytes.Buffer{},
		Stderr:  &stderr,
		Now: func() time.Time {
			return time.Date(2026, 3, 14, 10, 20, 30, 0, time.UTC)
		},
	}

	if err := app.Run(context.Background(), Options{TermQuery: "frontend", Since: time.Hour, OutDir: tempDir}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if matches, err := filepath.Glob(filepath.Join(tempDir, "frontend--team-a-*")); err != nil {
		t.Fatalf("Glob returned error: %v", err)
	} else if len(matches) != 0 {
		t.Fatalf("expected no output files, got %v", matches)
	}
	if strings.Contains(stderr.String(), "Writing ") {
		t.Fatalf("did not expect write message, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "No log lines found.\n") {
		t.Fatalf("expected explicit no-log message, got %q", stderr.String())
	}
}

func TestAppRunWritesYAMLWhenRequested(t *testing.T) {
	cluster := fakeCluster{
		workloads: []Workload{{
			Namespace: "team-a",
			Kind:      "Deployment",
			Name:      "frontend",
		}},
		targets: WorkloadTargets{Containers: []ContainerRef{
			{PodName: "frontend-a", ContainerName: "main"},
		}},
		logs: map[string][]LogEntry{
			"frontend-a/main": {{
				Timestamp:     "2026-03-14T10:00:00Z",
				PodName:       "frontend-a",
				ContainerName: "main",
				Line:          "2026-03-14T10:00:00Z first",
				Message:       "first",
			}},
		},
		followLogs: map[string][]LogEntry{
			"frontend-a/main": {{
				Timestamp:     "2026-03-14T10:00:02Z",
				PodName:       "frontend-a",
				ContainerName: "main",
				Line:          "2026-03-14T10:00:02Z live",
				Message:       "live",
			}},
		},
	}

	var stderr bytes.Buffer
	tempDir := t.TempDir()

	app := App{
		Cluster: cluster,
		Stdin:   strings.NewReader(""),
		Stdout:  &bytes.Buffer{},
		Stderr:  &stderr,
		Now: func() time.Time {
			return time.Date(2026, 3, 14, 10, 20, 30, 0, time.UTC)
		},
	}

	err := app.Run(context.Background(), Options{TermQuery: "frontend", Since: time.Hour, Output: OutputFormatYAML, OutDir: tempDir})
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

	if !strings.Contains(stderr.String(), "Writing 1 logs to "+filename) {
		t.Fatalf("expected stderr to mention yaml output file, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Log range: last 1h\n") {
		t.Fatalf("expected stderr to mention log range, got %q", stderr.String())
	}
}

func TestAppRunAddsMetadataWhenRequested(t *testing.T) {
	cluster := fakeCluster{
		workloads: []Workload{{
			Namespace: "team-a",
			Kind:      "Deployment",
			Name:      "frontend",
		}},
		targets: WorkloadTargets{Containers: []ContainerRef{
			{PodName: "frontend-a", ContainerName: "main"},
		}, RolloutWarning: "Warning: rollout appears to be in progress; logs include pods from multiple ReplicaSets: frontend-old, frontend-new"},
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

	app := App{
		Cluster: cluster,
		Stdin:   strings.NewReader(""),
		Stdout:  &bytes.Buffer{},
		Stderr:  &stderr,
		Now: func() time.Time {
			return time.Date(2026, 3, 14, 10, 20, 30, 0, time.UTC)
		},
	}

	err := app.Run(context.Background(), Options{TermQuery: "frontend", Since: time.Hour, Meta: true, OutDir: tempDir})
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
	if !strings.Contains(stderr.String(), "Warning: rollout appears to be in progress") {
		t.Fatalf("expected rollout warning in stderr, got %q", stderr.String())
	}
}

func TestAppRunPerContainerWritesSeparateFiles(t *testing.T) {
	cluster := fakeCluster{
		workloads: []Workload{{
			Namespace: "team-a",
			Kind:      "Deployment",
			Name:      "frontend",
		}},
		targetsByTarget: map[string]WorkloadTargets{
			"Deployment/team-a/frontend": {Containers: []ContainerRef{
				{PodName: "frontend-a", ContainerName: "main"},
				{PodName: "frontend-a", ContainerName: "sidecar"},
			}},
		},
		logs: map[string][]LogEntry{
			"frontend-a/main": {{
				Timestamp:     "2026-03-14T10:00:00Z",
				PodName:       "frontend-a",
				ContainerName: "main",
				Line:          "2026-03-14T10:00:00Z first",
				Message:       "first",
			}},
			"frontend-a/sidecar": {{
				Timestamp:     "2026-03-14T10:00:01Z",
				PodName:       "frontend-a",
				ContainerName: "sidecar",
				Line:          "2026-03-14T10:00:01Z second",
				Message:       "second",
			}},
		},
	}

	var stderr bytes.Buffer
	tempDir := t.TempDir()

	app := App{
		Cluster: cluster,
		Stdin:   strings.NewReader(""),
		Stdout:  &bytes.Buffer{},
		Stderr:  &stderr,
		Now: func() time.Time {
			return time.Date(2026, 3, 14, 10, 20, 30, 0, time.UTC)
		},
	}

	if err := app.Run(context.Background(), Options{TermQuery: "frontend", Since: time.Hour, PerContainer: true, OutDir: tempDir}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	mainFile := filepath.Join(tempDir, "frontend--team-a--frontend-a--main-2026-03-14_10-20-30Z.log")
	sidecarFile := filepath.Join(tempDir, "frontend--team-a--frontend-a--sidecar-2026-03-14_10-20-30Z.log")

	mainContent, err := os.ReadFile(mainFile)
	if err != nil {
		t.Fatalf("ReadFile main returned error: %v", err)
	}
	if string(mainContent) != "{\"kubernetes_timestamp\":\"2026-03-14T10:00:00Z\",\"message\":\"first\"}\n" {
		t.Fatalf("unexpected main file output:\n%s", string(mainContent))
	}

	sidecarContent, err := os.ReadFile(sidecarFile)
	if err != nil {
		t.Fatalf("ReadFile sidecar returned error: %v", err)
	}
	if string(sidecarContent) != "{\"kubernetes_timestamp\":\"2026-03-14T10:00:01Z\",\"message\":\"second\"}\n" {
		t.Fatalf("unexpected sidecar file output:\n%s", string(sidecarContent))
	}

	if !strings.Contains(stderr.String(), "Writing 1 logs to "+mainFile) {
		t.Fatalf("expected stderr to mention main container file, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Writing 1 logs to "+sidecarFile) {
		t.Fatalf("expected stderr to mention sidecar container file, got %q", stderr.String())
	}
}

func TestAppRunWritesToStdoutWhenRequested(t *testing.T) {
	cluster := fakeCluster{
		workloads: []Workload{{
			Namespace: "team-a",
			Kind:      "Deployment",
			Name:      "frontend",
		}},
		targetsByTarget: map[string]WorkloadTargets{
			"Deployment/team-a/frontend": {Containers: []ContainerRef{{PodName: "frontend-a", ContainerName: "main"}}},
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
		followLogs: map[string][]LogEntry{
			"frontend-a/main": {{
				Timestamp:     "2026-03-14T10:20:31Z",
				PodName:       "frontend-a",
				ContainerName: "main",
				Line:          "2026-03-14T10:20:31Z live",
				Message:       "live",
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
		Now:     func() time.Time { return time.Date(2026, 3, 14, 10, 20, 30, 0, time.UTC) },
	}

	if err := app.Run(context.Background(), Options{TermQuery: "frontend", Since: time.Hour, Stdout: true}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), `"message":"first"`) {
		t.Fatalf("expected stdout output, got %q", stdout.String())
	}
	if strings.Contains(stderr.String(), "Writing ") {
		t.Fatalf("did not expect file output message, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Containers: frontend-a/main\n") {
		t.Fatalf("expected stderr to contain container list, got %q", stderr.String())
	}
	if !strings.HasSuffix(stderr.String(), "\n\n") {
		t.Fatalf("expected stderr to end with a blank line, got %q", stderr.String())
	}
}

func TestAppRunFollowsLogsToStdout(t *testing.T) {
	cluster := fakeCluster{
		workloads: []Workload{{
			Namespace: "team-a",
			Kind:      "Deployment",
			Name:      "frontend",
		}},
		targetsByTarget: map[string]WorkloadTargets{
			"Deployment/team-a/frontend": {Containers: []ContainerRef{{PodName: "frontend-a", ContainerName: "main"}}},
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
		followLogs: map[string][]LogEntry{
			"frontend-a/main": {{
				Timestamp:     "2026-03-14T10:20:31Z",
				PodName:       "frontend-a",
				ContainerName: "main",
				Line:          "2026-03-14T10:20:31Z live",
				Message:       "live",
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
		Now:     func() time.Time { return time.Date(2026, 3, 14, 10, 20, 30, 0, time.UTC) },
	}

	if err := app.Run(context.Background(), Options{TermQuery: "frontend", Since: time.Hour, Stdout: true, Follow: true}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), `"message":"first"`) {
		t.Fatalf("expected followed stdout output, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"message":"live"`) {
		t.Fatalf("expected live followed stdout output, got %q", stdout.String())
	}
	if strings.Contains(stderr.String(), "Writing ") {
		t.Fatalf("did not expect file output message, got %q", stderr.String())
	}
}

func TestAppRunFollowsLogsAsYAMLDocuments(t *testing.T) {
	cluster := fakeCluster{
		workloads: []Workload{{
			Namespace: "team-a",
			Kind:      "Deployment",
			Name:      "frontend",
		}},
		targetsByTarget: map[string]WorkloadTargets{
			"Deployment/team-a/frontend": {Containers: []ContainerRef{{PodName: "frontend-a", ContainerName: "main"}}},
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
		followLogs: map[string][]LogEntry{
			"frontend-a/main": {{
				Timestamp:     "2026-03-14T10:20:31Z",
				PodName:       "frontend-a",
				ContainerName: "main",
				Line:          "2026-03-14T10:20:31Z live",
				Message:       "live",
			}},
		},
	}

	var stdout bytes.Buffer
	app := App{
		Cluster: cluster,
		Stdin:   strings.NewReader(""),
		Stdout:  &stdout,
		Stderr:  &bytes.Buffer{},
		Now:     func() time.Time { return time.Date(2026, 3, 14, 10, 20, 30, 0, time.UTC) },
	}

	if err := app.Run(context.Background(), Options{TermQuery: "frontend", Since: time.Hour, Follow: true, Output: OutputFormatYAML}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), "message: first\n") {
		t.Fatalf("expected historical yaml output, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "---\n") {
		t.Fatalf("expected yaml document separator, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "message: live\n") {
		t.Fatalf("expected live yaml output, got %q", stdout.String())
	}
}

func TestAppRunFollowAllowsMultipleTargetsAndSortsHistoricalLogs(t *testing.T) {
	app := App{
		Cluster: fakeCluster{
			workloads: []Workload{
				{Namespace: "team-a", Kind: "Deployment", Name: "frontend"},
				{Namespace: "team-a", Kind: "StatefulSet", Name: "database"},
			},
			targetsByTarget: map[string]WorkloadTargets{
				"Deployment/team-a/frontend":  {Containers: []ContainerRef{{PodName: "frontend-a", ContainerName: "main"}}},
				"StatefulSet/team-a/database": {Containers: []ContainerRef{{PodName: "database-0", ContainerName: "main"}}},
			},
			logs: map[string][]LogEntry{
				"frontend-a/main": {{Timestamp: "2026-03-14T10:00:01Z", PodName: "frontend-a", ContainerName: "main", Line: "2026-03-14T10:00:01Z second", Message: "second"}},
				"database-0/main": {{Timestamp: "2026-03-14T10:00:00Z", PodName: "database-0", ContainerName: "main", Line: "2026-03-14T10:00:00Z first", Message: "first"}},
			},
		},
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		Now:    func() time.Time { return time.Date(2026, 3, 14, 10, 20, 30, 0, time.UTC) },
	}

	stdout := app.Stdout.(*bytes.Buffer)
	err := app.Run(context.Background(), Options{All: true, Since: time.Hour, Stdout: true, Follow: true})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), stdout.String())
	}
	if !strings.Contains(lines[0], `"message":"first"`) {
		t.Fatalf("expected first historical line first, got %q", lines[0])
	}
	if !strings.Contains(lines[1], `"message":"second"`) {
		t.Fatalf("expected second historical line second, got %q", lines[1])
	}
}

func TestAppRunReportsWhenStdoutHasNoLogs(t *testing.T) {
	cluster := fakeCluster{
		workloads: []Workload{{
			Namespace: "team-a",
			Kind:      "Deployment",
			Name:      "frontend",
		}},
		targetsByTarget: map[string]WorkloadTargets{
			"Deployment/team-a/frontend": {Containers: []ContainerRef{{PodName: "frontend-a", ContainerName: "main"}}},
		},
		logs: map[string][]LogEntry{
			"frontend-a/main": nil,
		},
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := App{Cluster: cluster, Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr}

	if err := app.Run(context.Background(), Options{TermQuery: "frontend", Since: time.Hour, Stdout: true}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected empty stdout, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "No log lines found.\n") {
		t.Fatalf("expected explicit no-log message, got %q", stderr.String())
	}
}

func TestAppRunTailsCombinedEntries(t *testing.T) {
	cluster := fakeCluster{
		workloads: []Workload{{Namespace: "team-a", Kind: "Deployment", Name: "frontend"}},
		targetsByTarget: map[string]WorkloadTargets{
			"Deployment/team-a/frontend": {Containers: []ContainerRef{
				{PodName: "frontend-a", ContainerName: "main"},
				{PodName: "frontend-b", ContainerName: "main"},
			}},
		},
		logs: map[string][]LogEntry{
			"frontend-a/main": {{Timestamp: "2026-03-14T10:00:00Z", PodName: "frontend-a", ContainerName: "main", Line: "2026-03-14T10:00:00Z first", Message: "first"}},
			"frontend-b/main": {{Timestamp: "2026-03-14T10:00:01Z", PodName: "frontend-b", ContainerName: "main", Line: "2026-03-14T10:00:01Z second", Message: "second"}},
		},
	}

	var stdout bytes.Buffer
	app := App{Cluster: cluster, Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &bytes.Buffer{}}

	if err := app.Run(context.Background(), Options{TermQuery: "frontend", Since: time.Hour, Stdout: true, TailLines: 1, Output: OutputFormatRaw}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if strings.TrimSpace(stdout.String()) != "2026-03-14T10:00:01Z second" {
		t.Fatalf("unexpected tailed stdout: %q", stdout.String())
	}
}

func TestAppRunAllWritesSeparateFiles(t *testing.T) {
	cluster := fakeCluster{
		workloads: []Workload{
			{Namespace: "team-a", Kind: "Deployment", Name: "frontend"},
			{Namespace: "team-a", Kind: "StatefulSet", Name: "database"},
		},
		targetsByTarget: map[string]WorkloadTargets{
			"Deployment/team-a/frontend":  {Containers: []ContainerRef{{PodName: "frontend-a", ContainerName: "main"}}},
			"StatefulSet/team-a/database": {Containers: []ContainerRef{{PodName: "database-0", ContainerName: "main"}}},
		},
		logs: map[string][]LogEntry{
			"frontend-a/main": {{Timestamp: "2026-03-14T10:00:00Z", PodName: "frontend-a", ContainerName: "main", Line: "2026-03-14T10:00:00Z first", Message: "first"}},
			"database-0/main": {{Timestamp: "2026-03-14T10:00:01Z", PodName: "database-0", ContainerName: "main", Line: "2026-03-14T10:00:01Z second", Message: "second"}},
		},
	}

	tempDir := t.TempDir()

	app := App{
		Cluster: cluster,
		Stdin:   strings.NewReader(""),
		Stdout:  &bytes.Buffer{},
		Stderr:  &bytes.Buffer{},
		Now:     func() time.Time { return time.Date(2026, 3, 14, 10, 20, 30, 0, time.UTC) },
	}

	if err := app.Run(context.Background(), Options{All: true, Since: time.Hour, OutDir: tempDir}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	for _, name := range []string{
		"frontend--team-a-2026-03-14_10-20-30Z.log",
		"database--team-a-2026-03-14_10-20-30Z.log",
	} {
		if _, err := os.Stat(filepath.Join(tempDir, name)); err != nil {
			t.Fatalf("expected file %s: %v", name, err)
		}
	}
}

func TestAppRunPodModeUsesPods(t *testing.T) {
	cluster := fakeCluster{
		pods: []Workload{
			{Namespace: "kube-system", Kind: "Pod", Name: "kube-apiserver-node-1", Ready: 1, Desired: 1},
		},
		targetsByTarget: map[string]WorkloadTargets{
			"Pod/kube-system/kube-apiserver-node-1": {Containers: []ContainerRef{{PodName: "kube-apiserver-node-1", ContainerName: "kube-apiserver"}}},
		},
		logs: map[string][]LogEntry{
			"kube-apiserver-node-1/kube-apiserver": {{Timestamp: "2026-03-14T10:00:00Z", PodName: "kube-apiserver-node-1", ContainerName: "kube-apiserver", Line: "2026-03-14T10:00:00Z first", Message: "first"}},
		},
	}

	tempDir := t.TempDir()

	app := App{
		Cluster: cluster,
		Stdin:   strings.NewReader(""),
		Stdout:  &bytes.Buffer{},
		Stderr:  &bytes.Buffer{},
		Now:     func() time.Time { return time.Date(2026, 3, 14, 10, 20, 30, 0, time.UTC) },
	}

	if err := app.Run(context.Background(), Options{Pod: true, TermQuery: "apiserver", Since: time.Hour, OutDir: tempDir}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(tempDir, "kube-apiserver-node-1--kube-system-2026-03-14_10-20-30Z.log")); err != nil {
		t.Fatalf("expected pod output file: %v", err)
	}
}

func TestAppRunUsesBatchTargetResolverWhenAvailable(t *testing.T) {
	tempDir := t.TempDir()
	cluster := &trackingCluster{
		fakeCluster: fakeCluster{
			workloads: []Workload{
				{Namespace: "team-a", Kind: "Deployment", Name: "frontend"},
				{Namespace: "team-a", Kind: "StatefulSet", Name: "database"},
			},
			targetsByTarget: map[string]WorkloadTargets{
				"Deployment/team-a/frontend":  {Containers: []ContainerRef{{PodName: "frontend-a", ContainerName: "main"}}},
				"StatefulSet/team-a/database": {Containers: []ContainerRef{{PodName: "database-0", ContainerName: "main"}}},
			},
			logs: map[string][]LogEntry{
				"frontend-a/main": {{Timestamp: "2026-03-14T10:00:00Z", PodName: "frontend-a", ContainerName: "main", Line: "2026-03-14T10:00:00Z first", Message: "first"}},
				"database-0/main": {{Timestamp: "2026-03-14T10:00:01Z", PodName: "database-0", ContainerName: "main", Line: "2026-03-14T10:00:01Z second", Message: "second"}},
			},
		},
	}

	app := App{
		Cluster: cluster,
		Stdin:   strings.NewReader(""),
		Stdout:  &bytes.Buffer{},
		Stderr:  &bytes.Buffer{},
		Now:     func() time.Time { return time.Date(2026, 3, 14, 10, 20, 30, 0, time.UTC) },
	}

	if err := app.Run(context.Background(), Options{All: true, Since: time.Hour, OutDir: tempDir}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if cluster.resolveCalls != 1 {
		t.Fatalf("expected ResolveWorkloadTargets to be called once, got %d", cluster.resolveCalls)
	}
	if cluster.listContainerCalls != 0 {
		t.Fatalf("expected ListContainersForWorkload to be bypassed, got %d calls", cluster.listContainerCalls)
	}
}

func TestAppResolveWorkloadTargetsPassesNodeFilterToBatchResolver(t *testing.T) {
	cluster := &nodeTrackingCluster{
		resolvedTargets: map[string]WorkloadTargets{
			"Deployment/team-a/frontend": {Containers: []ContainerRef{{PodName: "frontend-a", ContainerName: "main"}}},
		},
	}
	app := App{Cluster: cluster}

	resolved, err := app.resolveWorkloadTargets(context.Background(), []Workload{{
		Namespace: "team-a",
		Kind:      "Deployment",
		Name:      "frontend",
	}}, Options{Node: "*worker*"})
	if err != nil {
		t.Fatalf("resolveWorkloadTargets returned error: %v", err)
	}
	if len(resolved) != 1 {
		t.Fatalf("expected 1 resolved target, got %d", len(resolved))
	}
	if cluster.resolveNode != "*worker*" {
		t.Fatalf("expected node filter to reach ResolveWorkloadTargets, got %q", cluster.resolveNode)
	}
}

func TestAppRunFailsWhenBatchResolverOmitsTarget(t *testing.T) {
	app := App{
		Cluster: missingTargetCluster{
			fakeCluster: fakeCluster{
				workloads: []Workload{
					{Namespace: "team-a", Kind: "Deployment", Name: "frontend"},
				},
			},
		},
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	}

	err := app.Run(context.Background(), Options{All: true, Since: time.Hour, OutDir: t.TempDir()})
	if err == nil {
		t.Fatal("expected an error")
	}
	if err.Error() != `missing resolved targets for Deployment/frontend in namespace "team-a"` {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAppRunLeavesNoTemporaryFilesAfterAtomicWrite(t *testing.T) {
	tempDir := t.TempDir()
	cluster := fakeCluster{
		workloads: []Workload{{
			Namespace: "team-a",
			Kind:      "Deployment",
			Name:      "frontend",
		}},
		targets: WorkloadTargets{Containers: []ContainerRef{
			{PodName: "frontend-a", ContainerName: "main"},
		}},
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

	app := App{
		Cluster: cluster,
		Stdin:   strings.NewReader(""),
		Stdout:  &bytes.Buffer{},
		Stderr:  &bytes.Buffer{},
		Now:     func() time.Time { return time.Date(2026, 3, 14, 10, 20, 30, 0, time.UTC) },
	}

	if err := app.Run(context.Background(), Options{TermQuery: "frontend", Since: time.Hour, OutDir: tempDir}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(tempDir, "*.tmp-*"))
	if err != nil {
		t.Fatalf("Glob returned error: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no temporary files, got %v", matches)
	}
}

func TestAppRunPropagatesContextCancellationToLogCollection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	app := App{
		Cluster: contextAwareCluster{
			fakeCluster: fakeCluster{
				workloads: []Workload{{
					Namespace: "team-a",
					Kind:      "Deployment",
					Name:      "frontend",
				}},
				targets: WorkloadTargets{Containers: []ContainerRef{
					{PodName: "frontend-a", ContainerName: "main"},
				}},
			},
		},
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	}

	err := app.Run(ctx, Options{TermQuery: "frontend", Since: time.Hour, Stdout: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

type fakeCluster struct {
	workloads       []Workload
	pods            []Workload
	standalonePods  []Workload
	targets         WorkloadTargets
	targetsByTarget map[string]WorkloadTargets
	logs            map[string][]LogEntry
	followLogs      map[string][]LogEntry
}

func (f fakeCluster) ListWorkloads(context.Context, string, string) ([]Workload, error) {
	return f.workloads, nil
}

func (f fakeCluster) ListPods(context.Context, string, string) ([]Workload, error) {
	return f.pods, nil
}

func (f fakeCluster) ListStandalonePods(context.Context, string, string) ([]Workload, error) {
	return f.standalonePods, nil
}

func (f fakeCluster) ListContainersForWorkload(_ context.Context, workload Workload, _ string) (WorkloadTargets, error) {
	if len(f.targetsByTarget) != 0 {
		return f.targetsByTarget[targetKey(workload)], nil
	}
	return f.targets, nil
}

func (f fakeCluster) ResolveWorkloadTargets(_ context.Context, workloads []Workload, node string) (map[string]WorkloadTargets, error) {
	resolved := make(map[string]WorkloadTargets, len(workloads))
	for _, workload := range workloads {
		targets, _ := f.ListContainersForWorkload(context.Background(), workload, node)
		resolved[targetKey(workload)] = targets
	}
	return resolved, nil
}

func (f fakeCluster) GetLogs(_ context.Context, _ string, podName, containerName string, _ time.Duration) ([]LogEntry, error) {
	return f.logs[podName+"/"+containerName], nil
}

func (f fakeCluster) FollowLogs(_ context.Context, _ string, podName, containerName string, _ time.Time, onEntry func(LogEntry) error) error {
	entries := f.followLogs[podName+"/"+containerName]
	for _, entry := range entries {
		if err := onEntry(entry); err != nil {
			return err
		}
	}
	return nil
}

func targetKey(workload Workload) string {
	return workloadKey(workload)
}

type trackingCluster struct {
	fakeCluster
	resolveCalls       int
	listContainerCalls int
}

func (c *trackingCluster) ResolveWorkloadTargets(ctx context.Context, workloads []Workload, node string) (map[string]WorkloadTargets, error) {
	c.resolveCalls++
	return c.fakeCluster.ResolveWorkloadTargets(ctx, workloads, node)
}

func (c *trackingCluster) ListContainersForWorkload(ctx context.Context, workload Workload, node string) (WorkloadTargets, error) {
	c.listContainerCalls++
	return c.fakeCluster.ListContainersForWorkload(ctx, workload, node)
}

type missingTargetCluster struct {
	fakeCluster
}

func (c missingTargetCluster) ResolveWorkloadTargets(context.Context, []Workload, string) (map[string]WorkloadTargets, error) {
	return map[string]WorkloadTargets{}, nil
}

type nodeTrackingCluster struct {
	workloads         []Workload
	resolvedTargets   map[string]WorkloadTargets
	listWorkloadsNode string
	resolveNode       string
}

func (c *nodeTrackingCluster) ListWorkloads(_ context.Context, _ string, node string) ([]Workload, error) {
	c.listWorkloadsNode = node
	return c.workloads, nil
}

func (c *nodeTrackingCluster) ListPods(context.Context, string, string) ([]Workload, error) {
	return nil, nil
}

func (c *nodeTrackingCluster) ListStandalonePods(context.Context, string, string) ([]Workload, error) {
	return nil, nil
}

func (c *nodeTrackingCluster) ListContainersForWorkload(context.Context, Workload, string) (WorkloadTargets, error) {
	return WorkloadTargets{}, nil
}

func (c *nodeTrackingCluster) ResolveWorkloadTargets(_ context.Context, _ []Workload, node string) (map[string]WorkloadTargets, error) {
	c.resolveNode = node
	return c.resolvedTargets, nil
}

func (c *nodeTrackingCluster) GetLogs(context.Context, string, string, string, time.Duration) ([]LogEntry, error) {
	return nil, nil
}

func (c *nodeTrackingCluster) FollowLogs(context.Context, string, string, string, time.Time, func(LogEntry) error) error {
	return nil
}

type contextAwareCluster struct {
	fakeCluster
}

func (c contextAwareCluster) GetLogs(ctx context.Context, _ string, _ string, _ string, _ time.Duration) ([]LogEntry, error) {
	return nil, ctx.Err()
}

func (c contextAwareCluster) FollowLogs(ctx context.Context, _ string, _ string, _ string, _ time.Time, _ func(LogEntry) error) error {
	return ctx.Err()
}
