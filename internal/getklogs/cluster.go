package getklogs

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type Cluster struct {
	client kubernetes.Interface
}

func NewCluster() (*Cluster, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	config, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes client: %w", err)
	}

	return &Cluster{client: clientset}, nil
}

func (c *Cluster) ListWorkloads(ctx context.Context, namespace string) ([]Workload, error) {
	scope := namespace
	if scope == "" {
		scope = metav1.NamespaceAll
	}

	deployments, err := c.client.AppsV1().Deployments(scope).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	daemonSets, err := c.client.AppsV1().DaemonSets(scope).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list daemonsets: %w", err)
	}
	statefulSets, err := c.client.AppsV1().StatefulSets(scope).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list statefulsets: %w", err)
	}

	workloads := make([]Workload, 0, len(deployments.Items)+len(daemonSets.Items)+len(statefulSets.Items))
	for _, item := range deployments.Items {
		workloads = append(workloads, Workload{
			Namespace: item.Namespace,
			Kind:      "Deployment",
			Name:      item.Name,
			Ready:     item.Status.ReadyReplicas,
			Desired:   replicasOrZero(item.Spec.Replicas),
		})
	}
	for _, item := range daemonSets.Items {
		workloads = append(workloads, Workload{
			Namespace: item.Namespace,
			Kind:      "DaemonSet",
			Name:      item.Name,
			Ready:     item.Status.NumberReady,
			Desired:   item.Status.DesiredNumberScheduled,
		})
	}
	for _, item := range statefulSets.Items {
		workloads = append(workloads, Workload{
			Namespace: item.Namespace,
			Kind:      "StatefulSet",
			Name:      item.Name,
			Ready:     item.Status.ReadyReplicas,
			Desired:   replicasOrZero(item.Spec.Replicas),
		})
	}

	slices.SortFunc(workloads, func(left, right Workload) int {
		switch {
		case left.Namespace < right.Namespace:
			return -1
		case left.Namespace > right.Namespace:
			return 1
		case left.Kind < right.Kind:
			return -1
		case left.Kind > right.Kind:
			return 1
		case left.Name < right.Name:
			return -1
		case left.Name > right.Name:
			return 1
		default:
			return 0
		}
	})

	return workloads, nil
}

func replicasOrZero(replicas *int32) int32 {
	if replicas == nil {
		return 0
	}
	return *replicas
}

func (c *Cluster) ListContainersForWorkload(ctx context.Context, workload Workload) ([]ContainerRef, error) {
	pods, err := c.client.CoreV1().Pods(workload.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	replicaSets, err := c.client.AppsV1().ReplicaSets(workload.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list replicasets: %w", err)
	}

	selectedPods := SelectPodsForWorkload(pods.Items, replicaSets.Items, workload)
	if len(selectedPods) == 0 {
		return nil, nil
	}

	var containers []ContainerRef
	for _, pod := range selectedPods {
		for _, name := range containerNamesFromStatus(pod) {
			containers = append(containers, ContainerRef{
				PodName:       pod.Name,
				ContainerName: name,
			})
		}
	}

	slices.SortFunc(containers, func(left, right ContainerRef) int {
		switch {
		case left.PodName < right.PodName:
			return -1
		case left.PodName > right.PodName:
			return 1
		case left.ContainerName < right.ContainerName:
			return -1
		case left.ContainerName > right.ContainerName:
			return 1
		default:
			return 0
		}
	})

	return containers, nil
}

func SelectPodsForWorkload(pods []corev1.Pod, replicaSets []appsv1.ReplicaSet, workload Workload) []corev1.Pod {
	replicaSetOwners := make(map[string]metav1.OwnerReference, len(replicaSets))
	for _, replicaSet := range replicaSets {
		owner, ok := firstControllerOwner(replicaSet.OwnerReferences)
		if ok {
			replicaSetOwners[replicaSet.Name] = owner
		}
	}

	var selected []corev1.Pod
	for _, pod := range pods {
		owner, ok := firstControllerOwner(pod.OwnerReferences)
		if !ok {
			continue
		}

		effectiveOwner := owner
		if owner.Kind == "ReplicaSet" {
			if replicaSetOwner, found := replicaSetOwners[owner.Name]; found {
				effectiveOwner = replicaSetOwner
			}
		}

		if effectiveOwner.Kind == workload.Kind && effectiveOwner.Name == workload.Name {
			selected = append(selected, pod)
		}
	}

	slices.SortFunc(selected, func(left, right corev1.Pod) int {
		return strings.Compare(left.Name, right.Name)
	})

	return selected
}

func firstControllerOwner(owners []metav1.OwnerReference) (metav1.OwnerReference, bool) {
	for _, owner := range owners {
		if owner.Controller != nil && *owner.Controller {
			return owner, true
		}
	}
	return metav1.OwnerReference{}, false
}

func containerNamesFromStatus(pod corev1.Pod) []string {
	seen := make(map[string]struct{})
	names := make([]string, 0, len(pod.Status.InitContainerStatuses)+len(pod.Status.ContainerStatuses)+len(pod.Status.EphemeralContainerStatuses))

	appendName := func(name string) {
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}

	for _, status := range pod.Status.InitContainerStatuses {
		appendName(status.Name)
	}
	for _, status := range pod.Status.ContainerStatuses {
		appendName(status.Name)
	}
	for _, status := range pod.Status.EphemeralContainerStatuses {
		appendName(status.Name)
	}

	return names
}

func (c *Cluster) GetLogs(ctx context.Context, namespace, podName, containerName string, since time.Duration) ([]LogEntry, error) {
	sinceTime := metav1.NewTime(time.Now().Add(-since))
	request := c.client.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		Container:  containerName,
		Timestamps: true,
		SinceTime:  &sinceTime,
	})

	stream, err := request.Stream(ctx)
	if err != nil {
		if ignoreLogError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get logs for pod %s container %s: %w", podName, containerName, err)
	}
	defer func() {
		_ = stream.Close()
	}()

	return readLogEntries(stream, podName, containerName)
}

func ignoreLogError(err error) bool {
	message := err.Error()
	return strings.Contains(message, "previous terminated container") ||
		strings.Contains(message, "container not found") ||
		strings.Contains(message, "PodInitializing") ||
		strings.Contains(message, "waiting to start")
}

func readLogEntries(reader io.Reader, podName, containerName string) ([]LogEntry, error) {
	scanner := bufio.NewScanner(reader)
	var entries []LogEntry
	for scanner.Scan() {
		line := scanner.Text()
		timestamp, err := splitTimestamp(line)
		if err != nil {
			return nil, fmt.Errorf("parse log line for pod %s container %s: %w", podName, containerName, err)
		}
		entries = append(entries, LogEntry{
			Timestamp:     timestamp,
			PodName:       podName,
			ContainerName: containerName,
			Line:          line,
			Message:       strings.TrimPrefix(line, timestamp+" "),
		})
	}
	if err := scanner.Err(); err != nil {
		if ignoreLogError(err) {
			return nil, nil
		}
		return nil, err
	}
	return entries, nil
}

func splitTimestamp(line string) (string, error) {
	index := strings.IndexByte(line, ' ')
	if index == -1 {
		return "", errors.New("missing timestamp")
	}
	return line[:index], nil
}
