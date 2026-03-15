package getklogs

import (
	"context"
	"fmt"
	"io"
	"slices"
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

type WorkloadTargets struct {
	Containers     []ContainerRef
	RolloutWarning string
}

type LogEntry struct {
	Timestamp     string
	PodName       string
	ContainerName string
	Line          string
	Message       string
}

type ClusterAPI interface {
	ListWorkloads(ctx context.Context, namespace, node string) ([]Workload, error)
	ListPods(ctx context.Context, namespace, node string) ([]Workload, error)
	ListStandalonePods(ctx context.Context, namespace, node string) ([]Workload, error)
	ListContainersForWorkload(ctx context.Context, workload Workload, node string) (WorkloadTargets, error)
	GetLogs(ctx context.Context, namespace, podName, containerName string, since time.Duration) ([]LogEntry, error)
	FollowLogs(ctx context.Context, namespace, podName, containerName string, startAt time.Time, onEntry func(LogEntry) error) error
}

type batchTargetResolver interface {
	ResolveWorkloadTargets(ctx context.Context, workloads []Workload, node string) (map[string]WorkloadTargets, error)
}

type App struct {
	Cluster        ClusterAPI
	Stdin          io.Reader
	Stdout         io.Writer
	Stderr         io.Writer
	ChooseWorkload WorkloadChooser
	Now            func() time.Time
}

type WorkloadChooser func(stdin io.Reader, stdout io.Writer, workloads []Workload) (Workload, error)

func (a App) Run(ctx context.Context, options Options) error {
	options = NormalizeOptions(options)
	if err := ValidateOptions(options); err != nil {
		return err
	}

	targets, err := a.listTargets(ctx, options)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return noTargetsFoundError(options)
	}

	selectedTargets, err := a.selectTargets(targets, options)
	if err != nil {
		return err
	}

	resolvedTargets, err := a.resolveWorkloadTargets(ctx, selectedTargets, options)
	if err != nil {
		return err
	}
	if options.Follow {
		return a.followTargets(ctx, selectedTargets, resolvedTargets, options)
	}

	for index, selected := range selectedTargets {
		targets, ok := resolvedTargets[workloadKey(selected)]
		if !ok {
			return fmt.Errorf("missing resolved targets for %s/%s in namespace %q", selected.Kind, selected.Name, selected.Namespace)
		}
		if err := a.runForTarget(ctx, selected, targets, options, index > 0); err != nil {
			return err
		}
	}

	return nil
}

func (a App) resolveWorkloadTargets(ctx context.Context, workloads []Workload, options Options) (map[string]WorkloadTargets, error) {
	if resolver, ok := a.Cluster.(batchTargetResolver); ok {
		return resolver.ResolveWorkloadTargets(ctx, workloads, options.Node)
	}

	resolved := make(map[string]WorkloadTargets, len(workloads))
	for _, workload := range workloads {
		targets, err := a.Cluster.ListContainersForWorkload(ctx, workload, options.Node)
		if err != nil {
			return nil, err
		}
		resolved[workloadKey(workload)] = targets
	}

	return resolved, nil
}

func workloadKey(workload Workload) string {
	return workload.Kind + "/" + workload.Namespace + "/" + workload.Name
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
