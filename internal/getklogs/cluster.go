package getklogs

import (
	"context"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

type Cluster struct {
	client kubernetes.Interface
}

func NewCluster(kubeconfig string) (*Cluster, error) {
	// client-go may emit throttling notices through klog; keep command output focused on getklogs itself.
	klog.LogToStderr(false)
	klog.SetOutput(io.Discard)

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	loadingRules.ExplicitPath = strings.TrimSpace(kubeconfig)
	configOverrides := &clientcmd.ConfigOverrides{}
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	config, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	config = configureRESTClient(config)

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("create kubernetes client: %w", err)
	}

	return &Cluster{client: clientset}, nil
}

func configureRESTClient(config *rest.Config) *rest.Config {
	// Kubernetes apiservers handle fairness and throttling themselves.
	// Disable the client-go limiter to avoid local throttling delays.
	config.QPS = -1
	config.RateLimiter = nil

	return config
}

func (c *Cluster) ListWorkloads(ctx context.Context, namespace, node string) ([]Workload, error) {
	scope := namespace
	if scope == "" {
		scope = metav1.NamespaceAll
	}

	var (
		deployments  *appsv1.DeploymentList
		daemonSets   *appsv1.DaemonSetList
		statefulSets *appsv1.StatefulSetList
	)

	group, ctx := errgroup.WithContext(ctx)
	group.Go(func() error {
		list, err := c.client.AppsV1().Deployments(scope).List(ctx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("list deployments: %w", err)
		}
		deployments = list
		return nil
	})
	group.Go(func() error {
		list, err := c.client.AppsV1().DaemonSets(scope).List(ctx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("list daemonsets: %w", err)
		}
		daemonSets = list
		return nil
	})
	group.Go(func() error {
		list, err := c.client.AppsV1().StatefulSets(scope).List(ctx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("list statefulsets: %w", err)
		}
		statefulSets = list
		return nil
	})

	if err := group.Wait(); err != nil {
		return nil, err
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

	if node != "" {
		pods, err := c.listPods(ctx, namespace)
		if err != nil {
			return nil, err
		}

		filteredPods := filterPodsByNode(pods, node)
		replicaSets, err := c.listReplicaSets(ctx, namespace)
		if err != nil {
			return nil, err
		}

		workloads = filterWorkloadsByPods(workloads, filteredPods, replicaSets)
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

func (c *Cluster) ListPods(ctx context.Context, namespace, node string) ([]Workload, error) {
	pods, err := c.listPods(ctx, namespace)
	if err != nil {
		return nil, err
	}
	pods = filterPodsByNode(pods, node)

	items := make([]Workload, 0, len(pods))
	for _, pod := range pods {
		items = append(items, podAsWorkload(pod))
	}
	return items, nil
}

func (c *Cluster) ListStandalonePods(ctx context.Context, namespace, node string) ([]Workload, error) {
	pods, err := c.listPods(ctx, namespace)
	if err != nil {
		return nil, err
	}
	pods = filterPodsByNode(pods, node)

	var items []Workload
	for _, pod := range pods {
		if _, ok := firstControllerOwner(pod.OwnerReferences); ok {
			continue
		}
		items = append(items, podAsWorkload(pod))
	}
	return items, nil
}

func (c *Cluster) listPods(ctx context.Context, namespace string) ([]corev1.Pod, error) {
	scope := namespace
	if scope == "" {
		scope = metav1.NamespaceAll
	}

	pods, err := c.client.CoreV1().Pods(scope).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}

	items := slices.Clone(pods.Items)
	slices.SortFunc(items, func(left, right corev1.Pod) int {
		switch {
		case left.Namespace < right.Namespace:
			return -1
		case left.Namespace > right.Namespace:
			return 1
		case left.Name < right.Name:
			return -1
		case left.Name > right.Name:
			return 1
		default:
			return 0
		}
	})

	return items, nil
}

func (c *Cluster) ListContainersForWorkload(ctx context.Context, workload Workload, node string) (WorkloadTargets, error) {
	pods, err := c.listPodsForNamespace(ctx, workload.Namespace)
	if err != nil {
		return WorkloadTargets{}, err
	}
	pods = filterPodsByNode(pods, node)
	if workload.Kind == "Pod" {
		return targetsForWorkload(pods, nil, workload), nil
	}

	replicaSets, err := c.listReplicaSetsForNamespace(ctx, workload.Namespace)
	if err != nil {
		return WorkloadTargets{}, err
	}

	return targetsForWorkload(pods, replicaSets, workload), nil
}

func (c *Cluster) ResolveWorkloadTargets(ctx context.Context, workloads []Workload, node string) (map[string]WorkloadTargets, error) {
	grouped := make(map[string][]Workload)
	for _, workload := range workloads {
		grouped[workload.Namespace] = append(grouped[workload.Namespace], workload)
	}

	resolved := make(map[string]WorkloadTargets, len(workloads))
	for namespace, namespaceWorkloads := range grouped {
		pods, err := c.listPodsForNamespace(ctx, namespace)
		if err != nil {
			return nil, err
		}
		pods = filterPodsByNode(pods, node)

		var replicaSets []appsv1.ReplicaSet
		if namespaceHasManagedWorkload(namespaceWorkloads) {
			replicaSets, err = c.listReplicaSetsForNamespace(ctx, namespace)
			if err != nil {
				return nil, err
			}
		}

		for _, workload := range namespaceWorkloads {
			resolved[workloadKey(workload)] = targetsForWorkload(pods, replicaSets, workload)
		}
	}

	return resolved, nil
}

func (c *Cluster) listReplicaSets(ctx context.Context, namespace string) ([]appsv1.ReplicaSet, error) {
	scope := namespace
	if scope == "" {
		scope = metav1.NamespaceAll
	}

	replicaSets, err := c.client.AppsV1().ReplicaSets(scope).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list replicasets: %w", err)
	}

	items := slices.Clone(replicaSets.Items)
	slices.SortFunc(items, func(left, right appsv1.ReplicaSet) int {
		switch {
		case left.Namespace < right.Namespace:
			return -1
		case left.Namespace > right.Namespace:
			return 1
		case left.Name < right.Name:
			return -1
		case left.Name > right.Name:
			return 1
		default:
			return 0
		}
	})

	return items, nil
}

func namespaceHasManagedWorkload(workloads []Workload) bool {
	for _, workload := range workloads {
		if workload.Kind != "Pod" {
			return true
		}
	}

	return false
}

func (c *Cluster) listPodsForNamespace(ctx context.Context, namespace string) ([]corev1.Pod, error) {
	pods, err := c.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}

	return pods.Items, nil
}

func (c *Cluster) listReplicaSetsForNamespace(ctx context.Context, namespace string) ([]appsv1.ReplicaSet, error) {
	replicaSets, err := c.client.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list replicasets: %w", err)
	}

	return replicaSets.Items, nil
}

func filterWorkloadsByPods(workloads []Workload, pods []corev1.Pod, replicaSets []appsv1.ReplicaSet) []Workload {
	filtered := make([]Workload, 0, len(workloads))
	for _, workload := range workloads {
		if len(SelectPodsForWorkload(pods, replicaSets, workload)) == 0 {
			continue
		}
		filtered = append(filtered, workload)
	}

	return filtered
}

func targetsForWorkload(pods []corev1.Pod, replicaSets []appsv1.ReplicaSet, workload Workload) WorkloadTargets {
	selectedPods := SelectPodsForWorkload(pods, replicaSets, workload)
	if len(selectedPods) == 0 {
		return WorkloadTargets{}
	}

	return buildTargetsForPods(selectedPods, workload)
}

func buildTargetsForPods(selectedPods []corev1.Pod, workload Workload) WorkloadTargets {
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

	return WorkloadTargets{
		Containers:     containers,
		RolloutWarning: rolloutWarningForWorkload(selectedPods, workload),
	}
}

func filterPodsByNode(pods []corev1.Pod, node string) []corev1.Pod {
	if node == "" {
		return pods
	}

	filtered := make([]corev1.Pod, 0, len(pods))
	for _, pod := range pods {
		if matchesNodePattern(pod.Spec.NodeName, node) {
			filtered = append(filtered, pod)
		}
	}

	return filtered
}

func matchesNodePattern(nodeName, pattern string) bool {
	if pattern == "" {
		return true
	}

	matched, err := path.Match(pattern, nodeName)
	if err != nil {
		return false
	}

	return matched
}

func SelectPodsForWorkload(pods []corev1.Pod, replicaSets []appsv1.ReplicaSet, workload Workload) []corev1.Pod {
	if workload.Kind == "Pod" {
		for _, pod := range pods {
			if pod.Namespace == workload.Namespace && pod.Name == workload.Name {
				return []corev1.Pod{pod}
			}
		}
		return nil
	}

	replicaSetOwners := make(map[string]metav1.OwnerReference, len(replicaSets))
	for _, replicaSet := range replicaSets {
		owner, ok := firstControllerOwner(replicaSet.OwnerReferences)
		if ok {
			replicaSetOwners[replicaSet.Namespace+"/"+replicaSet.Name] = owner
		}
	}

	var selected []corev1.Pod
	for _, pod := range pods {
		if pod.Namespace != workload.Namespace {
			continue
		}

		owner, ok := firstControllerOwner(pod.OwnerReferences)
		if !ok {
			continue
		}

		effectiveOwner := owner
		if owner.Kind == "ReplicaSet" {
			if replicaSetOwner, found := replicaSetOwners[pod.Namespace+"/"+owner.Name]; found {
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

func podAsWorkload(pod corev1.Pod) Workload {
	totalContainers := len(containerNamesFromStatus(pod))
	return Workload{
		Namespace: pod.Namespace,
		Kind:      "Pod",
		Name:      pod.Name,
		Ready:     int32(readyContainerCount(pod)),
		Desired:   int32(totalContainers),
	}
}

func readyContainerCount(pod corev1.Pod) int {
	count := 0
	for _, status := range pod.Status.InitContainerStatuses {
		if status.Ready {
			count++
		}
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Ready {
			count++
		}
	}
	for _, status := range pod.Status.EphemeralContainerStatuses {
		if status.Ready {
			count++
		}
	}
	return count
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

func rolloutWarningForWorkload(pods []corev1.Pod, workload Workload) string {
	switch workload.Kind {
	case "Deployment":
		replicaSets := ownerNamesForKind(pods, "ReplicaSet")
		if len(replicaSets) > 1 {
			return fmt.Sprintf("Warning: rollout appears to be in progress; logs include pods from multiple ReplicaSets: %s", strings.Join(replicaSets, ", "))
		}
	case "DaemonSet", "StatefulSet":
		revisions := uniqueLabelValues(pods, "controller-revision-hash")
		if len(revisions) > 1 {
			return fmt.Sprintf("Warning: rollout appears to be in progress; logs include pods from multiple revisions: %s", strings.Join(revisions, ", "))
		}
	}

	return ""
}

func ownerNamesForKind(pods []corev1.Pod, kind string) []string {
	set := make(map[string]struct{})
	for _, pod := range pods {
		owner, ok := firstControllerOwner(pod.OwnerReferences)
		if !ok || owner.Kind != kind || owner.Name == "" {
			continue
		}
		set[owner.Name] = struct{}{}
	}

	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func uniqueLabelValues(pods []corev1.Pod, key string) []string {
	set := make(map[string]struct{})
	for _, pod := range pods {
		value := pod.Labels[key]
		if value == "" {
			continue
		}
		set[value] = struct{}{}
	}

	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	slices.Sort(values)
	return values
}

func (c *Cluster) GetLogs(ctx context.Context, namespace, podName, containerName string, since time.Duration) ([]LogEntry, error) {
	request := c.client.CoreV1().Pods(namespace).GetLogs(podName, newPodLogOptions(containerName, since, time.Now(), false))

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

	var entries []LogEntry
	err = streamLogEntries(stream, podName, containerName, func(entry LogEntry) error {
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return entries, nil
}

func (c *Cluster) FollowLogs(ctx context.Context, namespace, podName, containerName string, startAt time.Time, onEntry func(LogEntry) error) error {
	request := c.client.CoreV1().Pods(namespace).GetLogs(podName, newFollowPodLogOptions(containerName, startAt))

	stream, err := request.Stream(ctx)
	if err != nil {
		if ignoreLogError(err) {
			return nil
		}
		return fmt.Errorf("follow logs for pod %s container %s: %w", podName, containerName, err)
	}
	defer func() {
		_ = stream.Close()
	}()

	return streamLogEntries(stream, podName, containerName, onEntry)
}

func newPodLogOptions(containerName string, since time.Duration, now time.Time, follow bool) *corev1.PodLogOptions {
	options := &corev1.PodLogOptions{
		Container:  containerName,
		Timestamps: true,
		Follow:     follow,
	}
	if since > 0 {
		sinceTime := metav1.NewTime(now.Add(-since))
		options.SinceTime = &sinceTime
	}

	return options
}

func newFollowPodLogOptions(containerName string, startAt time.Time) *corev1.PodLogOptions {
	options := &corev1.PodLogOptions{
		Container:  containerName,
		Timestamps: true,
		Follow:     true,
	}
	if !startAt.IsZero() {
		sinceTime := metav1.NewTime(startAt)
		options.SinceTime = &sinceTime
	}

	return options
}

func ignoreLogError(err error) bool {
	if apierrors.IsNotFound(err) || apierrors.IsBadRequest(err) {
		return true
	}

	message := err.Error()
	return strings.Contains(message, "previous terminated container") ||
		strings.Contains(message, "container not found") ||
		strings.Contains(message, "PodInitializing") ||
		strings.Contains(message, "waiting to start")
}

func readLogEntries(reader io.Reader, podName, containerName string) ([]LogEntry, error) {
	var entries []LogEntry
	err := streamLogEntries(reader, podName, containerName, func(entry LogEntry) error {
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return entries, nil
}

func streamLogEntries(reader io.Reader, podName, containerName string, onEntry func(LogEntry) error) error {
	scanner := newLineScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		timestamp, message, ok := splitStructuredTimestamp(line)
		if !ok {
			return fmt.Errorf("parse log line for pod %s container %s: missing RFC3339 timestamp", podName, containerName)
		}
		if err := onEntry(LogEntry{
			Timestamp:     timestamp,
			PodName:       podName,
			ContainerName: containerName,
			Line:          line,
			Message:       message,
		}); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		if ignoreLogError(err) {
			return nil
		}
		return err
	}
	return nil
}
