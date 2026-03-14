package getklogs

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

type Workload struct {
	Namespace string
	Kind      string
	Name      string
	Ready     int32
	Desired   int32
}

func (w Workload) ReadyText() string {
	return fmt.Sprintf("%d/%d", w.Ready, w.Desired)
}

type ContainerRef struct {
	PodName       string
	ContainerName string
}

type LogEntry struct {
	Timestamp     string
	PodName       string
	ContainerName string
	Line          string
}

type ClusterAPI interface {
	ListWorkloads(ctx context.Context, namespace string) ([]Workload, error)
	ListContainersForWorkload(ctx context.Context, workload Workload) ([]ContainerRef, error)
	GetLogs(ctx context.Context, namespace, podName, containerName string, since time.Duration) ([]LogEntry, error)
}

type App struct {
	Cluster ClusterAPI
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
}

func (a App) Run(ctx context.Context, options Options) error {
	workloads, err := a.Cluster.ListWorkloads(ctx, options.Namespace)
	if err != nil {
		return err
	}
	if len(workloads) == 0 {
		if options.Namespace != "" {
			return fmt.Errorf("no Deployment/DaemonSet/StatefulSet found in namespace %q", options.Namespace)
		}
		return errors.New("no Deployment/DaemonSet/StatefulSet found in any namespace")
	}

	selected, err := a.selectWorkload(workloads, options.TermQuery)
	if err != nil {
		return err
	}

	containers, err := a.Cluster.ListContainersForWorkload(ctx, selected)
	if err != nil {
		return err
	}
	if len(containers) == 0 {
		return fmt.Errorf("no pods found for %s/%s in namespace %q", selected.Kind, selected.Name, selected.Namespace)
	}

	podNames := uniquePodNames(containers)
	if _, err := fmt.Fprintf(a.Stderr, "Running for namespace %q on %s/%s\n", selected.Namespace, selected.Kind, selected.Name); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(a.Stderr, "Pods:"); err != nil {
		return err
	}
	for _, podName := range podNames {
		if _, err := fmt.Fprintln(a.Stderr, podName); err != nil {
			return err
		}
	}

	entries, err := a.collectLogs(ctx, selected.Namespace, containers, options.Since)
	if err != nil {
		return err
	}

	lines := renderEntries(entries)
	if options.OutputFile != "" {
		if _, err := fmt.Fprintf(a.Stderr, "Writing logs to %q.\n", options.OutputFile); err != nil {
			return err
		}
		if len(lines) == 0 {
			return os.WriteFile(options.OutputFile, nil, 0o644)
		}
		return os.WriteFile(options.OutputFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	}

	if len(lines) == 0 {
		return nil
	}

	_, err = io.WriteString(a.Stdout, strings.Join(lines, "\n")+"\n")
	return err
}

func (a App) selectWorkload(workloads []Workload, term string) (Workload, error) {
	if term != "" {
		matches := FilterWorkloads(workloads, term)
		if len(matches) == 0 {
			return Workload{}, fmt.Errorf("no workload matches '*%s*'", term)
		}
		if len(matches) > 1 {
			var builder strings.Builder
			fmt.Fprintf(&builder, "multiple workloads match '*%s*':\n", term)
			for _, match := range matches {
				fmt.Fprintf(&builder, "%s\t%s\t%s\t%s\n", match.Namespace, match.Kind, match.Name, match.ReadyText())
			}
			return Workload{}, errors.New(strings.TrimRight(builder.String(), "\n"))
		}
		return matches[0], nil
	}

	return chooseWorkloadInteractively(a.Stdin, a.Stdout, workloads)
}

func chooseWorkloadInteractively(stdin io.Reader, stdout io.Writer, workloads []Workload) (Workload, error) {
	if _, err := fmt.Fprintln(stdout, "Select workload:"); err != nil {
		return Workload{}, err
	}
	for index, workload := range workloads {
		if _, err := fmt.Fprintf(stdout, "%3d. %s\t%s\t%s\t%s\n", index+1, workload.Namespace, workload.Kind, workload.Name, workload.ReadyText()); err != nil {
			return Workload{}, err
		}
	}

	reader := bufio.NewReader(stdin)
	for {
		if _, err := fmt.Fprintf(stdout, "Enter number [1-%d]: ", len(workloads)); err != nil {
			return Workload{}, err
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			return Workload{}, fmt.Errorf("reading selection: %w", err)
		}

		selection, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || selection < 1 || selection > len(workloads) {
			if _, writeErr := fmt.Fprintln(stdout, "Invalid selection."); writeErr != nil {
				return Workload{}, writeErr
			}
			continue
		}

		return workloads[selection-1], nil
	}
}

func FilterWorkloads(workloads []Workload, term string) []Workload {
	needle := strings.ToLower(term)
	var matches []Workload
	for _, workload := range workloads {
		haystack := strings.ToLower(workload.Namespace + "\t" + workload.Kind + "\t" + workload.Name)
		if strings.Contains(haystack, needle) {
			matches = append(matches, workload)
		}
	}
	return matches
}

func uniquePodNames(containers []ContainerRef) []string {
	set := make(map[string]struct{}, len(containers))
	for _, container := range containers {
		set[container.PodName] = struct{}{}
	}

	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func (a App) collectLogs(ctx context.Context, namespace string, containers []ContainerRef, since time.Duration) ([]LogEntry, error) {
	var (
		group   errgroup.Group
		mu      sync.Mutex
		entries []LogEntry
	)

	group.SetLimit(8)

	for _, container := range containers {
		container := container
		group.Go(func() error {
			logs, err := a.Cluster.GetLogs(ctx, namespace, container.PodName, container.ContainerName, since)
			if err != nil {
				return err
			}
			mu.Lock()
			entries = append(entries, logs...)
			mu.Unlock()
			return nil
		})
	}

	if err := group.Wait(); err != nil {
		return nil, err
	}

	slices.SortFunc(entries, func(left, right LogEntry) int {
		switch {
		case left.Timestamp < right.Timestamp:
			return -1
		case left.Timestamp > right.Timestamp:
			return 1
		case left.PodName < right.PodName:
			return -1
		case left.PodName > right.PodName:
			return 1
		case left.ContainerName < right.ContainerName:
			return -1
		case left.ContainerName > right.ContainerName:
			return 1
		case left.Line < right.Line:
			return -1
		case left.Line > right.Line:
			return 1
		default:
			return 0
		}
	})

	return entries, nil
}

func renderEntries(entries []LogEntry) []string {
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		lines = append(lines, fmt.Sprintf("%s %s %s", entry.PodName, entry.ContainerName, entry.Line))
	}
	return lines
}
