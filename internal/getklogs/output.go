package getklogs

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

func (a App) runForTarget(ctx context.Context, selected Workload, targets WorkloadTargets, options Options, hasPreviousStdoutOutput bool) error {
	containers := targets.Containers
	if len(containers) == 0 {
		return fmt.Errorf("no pods found for %s/%s in namespace %q", selected.Kind, selected.Name, selected.Namespace)
	}

	if err := a.printTargetHeader(selected, targets, options); err != nil {
		return err
	}

	if options.PerContainer {
		return a.runForTargetPerContainer(ctx, selected, containers, options)
	}

	entries, err := a.collectLogs(ctx, selected.Namespace, containers, options.Since)
	if err != nil {
		return err
	}
	if options.TailLines > 0 && len(entries) > options.TailLines {
		entries = entries[len(entries)-options.TailLines:]
	}

	content, err := renderOutput(entries, options)
	if err != nil {
		return err
	}
	logCount := len(entries)

	if options.Stdout {
		if len(content) == 0 {
			if _, err := fmt.Fprintln(a.Stderr, "No log lines found."); err != nil {
				return err
			}
			_, err := fmt.Fprintln(a.Stderr)
			return err
		}
		if hasPreviousStdoutOutput && options.Output == OutputFormatYAML {
			if _, err := io.WriteString(a.Stdout, "---\n"); err != nil {
				return err
			}
		}
		if _, err := a.Stdout.Write(content); err != nil {
			return err
		}
		_, err = fmt.Fprintln(a.Stderr)
		return err
	}

	if logCount == 0 {
		if _, err := fmt.Fprintln(a.Stderr, "No log lines found."); err != nil {
			return err
		}
		_, err := fmt.Fprintln(a.Stderr)
		return err
	}

	if err := os.MkdirAll(options.OutDir, 0o755); err != nil {
		return fmt.Errorf("create output directory %q: %w", options.OutDir, err)
	}

	outputFile := buildOutputPath(selected, a.now().UTC(), options)

	if _, err := fmt.Fprintf(a.Stderr, "Writing %d logs to %s\n", logCount, outputFile); err != nil {
		return err
	}

	if err := writeFileAtomically(outputFile, content, 0o644); err != nil {
		return err
	}

	_, err = fmt.Fprintln(a.Stderr)
	return err
}

func (a App) followTargets(ctx context.Context, selectedTargets []Workload, resolvedTargets map[string]WorkloadTargets, options Options) error {
	var (
		historical []LogEntry
		containers []followContainer
		wroteAny   bool
	)

	startAt := a.now().UTC()

	for _, selected := range selectedTargets {
		targets, ok := resolvedTargets[workloadKey(selected)]
		if !ok {
			return fmt.Errorf("missing resolved targets for %s/%s in namespace %q", selected.Kind, selected.Name, selected.Namespace)
		}
		if len(targets.Containers) == 0 {
			return fmt.Errorf("no pods found for %s/%s in namespace %q", selected.Kind, selected.Name, selected.Namespace)
		}
		if err := a.printTargetHeader(selected, targets, options); err != nil {
			return err
		}

		entries, err := a.collectLogs(ctx, selected.Namespace, targets.Containers, options.Since)
		if err != nil {
			return err
		}
		historical = append(historical, filterEntriesBeforeTime(entries, startAt)...)
		for _, container := range targets.Containers {
			containers = append(containers, followContainer{
				namespace: selected.Namespace,
				ref:       container,
			})
		}
	}

	sortLogEntries(historical)
	for _, entry := range historical {
		if err := a.writeFollowEntry(entry, options, &wroteAny); err != nil {
			return err
		}
	}

	var (
		group errgroup.Group
		mu    sync.Mutex
	)

	group.SetLimit(8)

	for _, container := range containers {
		container := container
		group.Go(func() error {
			return a.Cluster.FollowLogs(ctx, container.namespace, container.ref.PodName, container.ref.ContainerName, startAt, func(entry LogEntry) error {
				mu.Lock()
				defer mu.Unlock()
				return a.writeFollowEntry(entry, options, &wroteAny)
			})
		})
	}

	if err := group.Wait(); err != nil {
		return err
	}
	if !wroteAny {
		if _, err := fmt.Fprintln(a.Stderr, "No log lines found."); err != nil {
			return err
		}
	}

	_, err := fmt.Fprintln(a.Stderr)
	return err
}

func (a App) writeFollowEntry(entry LogEntry, options Options, wroteAny *bool) error {
	line, err := renderEntry(entry, options)
	if err != nil {
		return err
	}
	if options.Output == OutputFormatYAML && *wroteAny {
		if _, err := io.WriteString(a.Stdout, "---\n"); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(a.Stdout, line+"\n"); err != nil {
		return err
	}
	*wroteAny = true
	return nil
}

type followContainer struct {
	namespace string
	ref       ContainerRef
}

func (a App) runForTargetPerContainer(ctx context.Context, selected Workload, containers []ContainerRef, options Options) error {
	var wroteAny bool
	now := a.now().UTC()

	for _, container := range containers {
		entries, err := a.collectLogs(ctx, selected.Namespace, []ContainerRef{container}, options.Since)
		if err != nil {
			return err
		}
		if options.TailLines > 0 && len(entries) > options.TailLines {
			entries = entries[len(entries)-options.TailLines:]
		}

		content, err := renderOutput(entries, options)
		if err != nil {
			return err
		}
		logCount := len(entries)
		if logCount == 0 {
			continue
		}

		if err := os.MkdirAll(options.OutDir, 0o755); err != nil {
			return fmt.Errorf("create output directory %q: %w", options.OutDir, err)
		}

		outputFile := buildOutputPathForContainer(selected, container, now, options)
		if _, err := fmt.Fprintf(a.Stderr, "Writing %d logs to %s\n", logCount, outputFile); err != nil {
			return err
		}
		if err := writeFileAtomically(outputFile, content, 0o644); err != nil {
			return err
		}

		wroteAny = true
	}

	if !wroteAny {
		if _, err := fmt.Fprintln(a.Stderr, "No log lines found."); err != nil {
			return err
		}
	}

	_, err := fmt.Fprintln(a.Stderr)
	return err
}

func writeFileAtomically(filename string, content []byte, mode os.FileMode) error {
	tempFile, err := os.CreateTemp(filepath.Dir(filename), filepath.Base(filename)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", filename, err)
	}
	tempName := tempFile.Name()
	cleanupTemp := true
	defer func() {
		_ = tempFile.Close()
		if cleanupTemp {
			_ = os.Remove(tempName)
		}
	}()

	if err := tempFile.Chmod(mode); err != nil {
		return fmt.Errorf("set mode on temporary file for %s: %w", filename, err)
	}
	if len(content) > 0 {
		if _, err := tempFile.Write(content); err != nil {
			return fmt.Errorf("write temporary file for %s: %w", filename, err)
		}
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("close temporary file for %s: %w", filename, err)
	}
	if err := os.Rename(tempName, filename); err != nil {
		return fmt.Errorf("replace %s: %w", filename, err)
	}

	cleanupTemp = false
	return nil
}

func buildOutputFilename(workload Workload, now time.Time, outputFormat string) string {
	extension := ".log"
	if outputFormat == OutputFormatYAML {
		extension = ".yaml"
	}

	return fmt.Sprintf("%s--%s-%s%s", workload.Name, workload.Namespace, now.UTC().Format("2006-01-02_15-04-05Z"), extension)
}

func buildOutputPath(workload Workload, now time.Time, options Options) string {
	return filepath.Join(options.OutDir, buildOutputFilename(workload, now, options.Output))
}

func buildOutputFilenameForContainer(workload Workload, container ContainerRef, now time.Time, outputFormat string) string {
	extension := ".log"
	if outputFormat == OutputFormatYAML {
		extension = ".yaml"
	}

	return fmt.Sprintf(
		"%s--%s--%s--%s-%s%s",
		workload.Name,
		workload.Namespace,
		container.PodName,
		container.ContainerName,
		now.UTC().Format("2006-01-02_15-04-05Z"),
		extension,
	)
}

func buildOutputPathForContainer(workload Workload, container ContainerRef, now time.Time, options Options) string {
	return filepath.Join(options.OutDir, buildOutputFilenameForContainer(workload, container, now, options.Output))
}

func (a App) printTargetHeader(selected Workload, targets WorkloadTargets, options Options) error {
	if _, err := fmt.Fprintf(a.Stderr, "Running for namespace %q on %s/%s\n", selected.Namespace, selected.Kind, selected.Name); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.Stderr, "Log range: %s\n", DescribeSinceWindow(options.Since)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.Stderr, "Containers: %s\n", formatContainerRefs(targets.Containers)); err != nil {
		return err
	}
	if targets.RolloutWarning != "" {
		if _, err := fmt.Fprintln(a.Stderr, targets.RolloutWarning); err != nil {
			return err
		}
	}

	return nil
}

func filterEntriesBeforeTime(entries []LogEntry, cutoff time.Time) []LogEntry {
	filtered := make([]LogEntry, 0, len(entries))
	for _, entry := range entries {
		timestamp, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
		if err != nil {
			filtered = append(filtered, entry)
			continue
		}
		if timestamp.Before(cutoff) {
			filtered = append(filtered, entry)
		}
	}

	return filtered
}

func sortLogEntries(entries []LogEntry) {
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
}

func (a App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}

	return time.Now()
}

func formatContainerRefs(containers []ContainerRef) string {
	parts := make([]string, 0, len(containers))
	for _, container := range containers {
		parts = append(parts, container.PodName+"/"+container.ContainerName)
	}
	slices.Sort(parts)

	return strings.Join(parts, " ")
}
