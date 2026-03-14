package getklogs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/manifoldco/promptui"
	"go.yaml.in/yaml/v3"
	"golang.org/x/sync/errgroup"
	xterm "golang.org/x/term"
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
	ListWorkloads(ctx context.Context, namespace string) ([]Workload, error)
	ListPods(ctx context.Context, namespace string) ([]Workload, error)
	ListStandalonePods(ctx context.Context, namespace string) ([]Workload, error)
	ListContainersForWorkload(ctx context.Context, workload Workload) (WorkloadTargets, error)
	GetLogs(ctx context.Context, namespace, podName, containerName string, since time.Duration) ([]LogEntry, error)
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

	for index, selected := range selectedTargets {
		if err := a.runForTarget(ctx, selected, options, index > 0); err != nil {
			return err
		}
	}

	return nil
}

func (a App) runForTarget(ctx context.Context, selected Workload, options Options, hasPreviousStdoutOutput bool) error {
	targets, err := a.Cluster.ListContainersForWorkload(ctx, selected)
	if err != nil {
		return err
	}
	containers := targets.Containers
	if len(containers) == 0 {
		return fmt.Errorf("no pods found for %s/%s in namespace %q", selected.Kind, selected.Name, selected.Namespace)
	}

	podNames := uniquePodNames(containers)
	if _, err := fmt.Fprintf(a.Stderr, "Running for namespace %q on %s/%s\n", selected.Namespace, selected.Kind, selected.Name); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(a.Stderr, "Pods: %s\n", strings.Join(podNames, " ")); err != nil {
		return err
	}
	if targets.RolloutWarning != "" {
		if _, err := fmt.Fprintln(a.Stderr, targets.RolloutWarning); err != nil {
			return err
		}
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

	if options.Stdout {
		if len(content) == 0 {
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

	outputFile := buildOutputFilename(selected, a.now().UTC(), options.Output)

	if _, err := fmt.Fprintf(a.Stderr, "Writing logs to %s\n", outputFile); err != nil {
		return err
	}

	var writeErr error
	if len(content) == 0 {
		writeErr = os.WriteFile(outputFile, nil, 0o644)
	} else {
		writeErr = os.WriteFile(outputFile, content, 0o644)
	}
	if writeErr != nil {
		return writeErr
	}

	_, err = fmt.Fprintln(a.Stderr)
	return err
}

func (a App) listTargets(ctx context.Context, options Options) ([]Workload, error) {
	if options.Pod {
		return a.Cluster.ListPods(ctx, options.Namespace)
	}

	workloads, err := a.Cluster.ListWorkloads(ctx, options.Namespace)
	if err != nil {
		return nil, err
	}
	if !options.All {
		return workloads, nil
	}

	standalonePods, err := a.Cluster.ListStandalonePods(ctx, options.Namespace)
	if err != nil {
		return nil, err
	}

	return append(workloads, standalonePods...), nil
}

func noTargetsFoundError(options Options) error {
	switch {
	case options.Pod && options.Namespace != "":
		return fmt.Errorf("no pods found in namespace %q", options.Namespace)
	case options.Pod:
		return errors.New("no pods found in any namespace")
	case options.All && options.Namespace != "":
		return fmt.Errorf("no Deployment/DaemonSet/StatefulSet or standalone pod found in namespace %q", options.Namespace)
	case options.All:
		return errors.New("no Deployment/DaemonSet/StatefulSet or standalone pod found in any namespace")
	case options.Namespace != "":
		return fmt.Errorf("no Deployment/DaemonSet/StatefulSet found in namespace %q", options.Namespace)
	default:
		return errors.New("no Deployment/DaemonSet/StatefulSet found in any namespace")
	}
}

func (a App) selectTargets(targets []Workload, options Options) ([]Workload, error) {
	if options.TermQuery != "" {
		matches := FilterWorkloads(targets, options.TermQuery)
		if len(matches) == 0 {
			return nil, fmt.Errorf("no target matches '*%s*'", options.TermQuery)
		}
		if options.All {
			return matches, nil
		}
		if len(matches) == 1 {
			return matches, nil
		}

		selected, err := a.chooseWorkload(matches)
		if err != nil {
			return nil, err
		}
		return []Workload{selected}, nil
	}

	if options.All {
		return targets, nil
	}

	selected, err := a.chooseWorkload(targets)
	if err != nil {
		return nil, err
	}
	return []Workload{selected}, nil
}

func (a App) chooseWorkload(workloads []Workload) (Workload, error) {
	chooser := a.ChooseWorkload
	if chooser == nil {
		chooser = chooseWorkloadInteractively
	}

	return chooser(a.Stdin, a.Stdout, workloads)
}

func chooseWorkloadInteractively(stdin io.Reader, stdout io.Writer, workloads []Workload) (Workload, error) {
	if canUseInteractivePrompt(stdin, stdout) {
		return chooseWorkloadWithPrompt(stdin, stdout, workloads)
	}

	return chooseWorkloadByNumber(stdin, stdout, workloads)
}

func chooseWorkloadByNumber(stdin io.Reader, stdout io.Writer, workloads []Workload) (Workload, error) {
	if _, err := fmt.Fprintln(stdout, "Select workload:"); err != nil {
		return Workload{}, err
	}
	for index, workload := range workloads {
		if _, err := fmt.Fprintf(stdout, "%3d. %s\n", index+1, formatWorkload(workload)); err != nil {
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

var runWorkloadPrompt = func(stdin io.ReadCloser, stdout io.WriteCloser, workloads []Workload) (int, string, error) {
	prompt := promptui.Select{
		Label:             "Select workload",
		Items:             workloads,
		Size:              min(10, len(workloads)),
		StartInSearchMode: true,
		HideHelp:          true,
		HideSelected:      true,
		Templates: &promptui.SelectTemplates{
			Label:    "{{ . | bold }}",
			Active:   promptui.IconSelect + ` {{ .Name | cyan | bold }} {{ "|" | faint }} {{ .Namespace | faint }} {{ "|" | faint }} {{ .Kind | faint }} {{ "|" | faint }} {{ .ReadyText | faint }}`,
			Inactive: `  {{ .Name }} {{ "|" | faint }} {{ .Namespace | faint }} {{ "|" | faint }} {{ .Kind | faint }} {{ "|" | faint }} {{ .ReadyText | faint }}`,
			Selected: `{{ .Name | cyan | bold }}`,
		},
		Searcher: func(input string, index int) bool {
			return matchWorkloadSearch(input, workloads[index])
		},
		Stdin:  wrapReadCloser(stdin),
		Stdout: wrapWriteCloser(stdout),
	}

	return prompt.Run()
}

func chooseWorkloadWithPrompt(stdin io.Reader, stdout io.Writer, workloads []Workload) (Workload, error) {
	index, _, err := runWorkloadPrompt(wrapReadCloser(stdin), wrapWriteCloser(stdout), workloads)
	if err != nil {
		if errors.Is(err, promptui.ErrInterrupt) || errors.Is(err, promptui.ErrEOF) {
			return Workload{}, errors.New("workload selection aborted")
		}
		return Workload{}, fmt.Errorf("interactive workload selection failed: %w", err)
	}

	return workloads[index], nil
}

func formatWorkload(workload Workload) string {
	return fmt.Sprintf("%s | %s | %s | %s", workload.Name, workload.Namespace, workload.Kind, workload.ReadyText())
}

func buildOutputFilename(workload Workload, now time.Time, outputFormat string) string {
	extension := ".log"
	if outputFormat == OutputFormatYAML {
		extension = ".yaml"
	}

	return fmt.Sprintf("%s--%s-%s%s", workload.Name, workload.Namespace, now.UTC().Format("2006-01-02_15-04-05Z"), extension)
}

func (a App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}

	return time.Now()
}

func matchWorkloadSearch(input string, workload Workload) bool {
	needle := strings.ToLower(strings.TrimSpace(input))
	if needle == "" {
		return true
	}

	haystack := strings.ToLower(strings.Join([]string{
		workload.Name,
		workload.Namespace,
		workload.Kind,
		workload.ReadyText(),
	}, " "))

	return strings.Contains(haystack, needle)
}

func canUseInteractivePrompt(stdin io.Reader, stdout io.Writer) bool {
	return isTerminal(stdin) && isTerminal(stdout)
}

type nopReadCloser struct {
	io.Reader
}

func (nopReadCloser) Close() error {
	return nil
}

func wrapReadCloser(reader io.Reader) io.ReadCloser {
	if readCloser, ok := reader.(io.ReadCloser); ok {
		return readCloser
	}

	return nopReadCloser{Reader: reader}
}

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error {
	return nil
}

func wrapWriteCloser(writer io.Writer) io.WriteCloser {
	if writeCloser, ok := writer.(io.WriteCloser); ok {
		return writeCloser
	}

	return nopWriteCloser{Writer: writer}
}

type fdProvider interface {
	Fd() uintptr
}

func isTerminal(stream any) bool {
	fd, ok := stream.(fdProvider)
	if !ok {
		return false
	}

	return xterm.IsTerminal(int(fd.Fd()))
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

func renderOutput(entries []LogEntry, options Options) ([]byte, error) {
	switch options.Output {
	case OutputFormatJSON:
		lines, err := renderEntries(entries, options)
		if err != nil {
			return nil, err
		}
		if len(lines) == 0 {
			return nil, nil
		}
		return []byte(strings.Join(lines, "\n") + "\n"), nil
	case OutputFormatYAML:
		return renderYAMLOutput(entries, options)
	default:
		return nil, fmt.Errorf("unsupported output format %q", options.Output)
	}
}

func renderEntries(entries []LogEntry, options Options) ([]string, error) {
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		line, err := renderEntry(entry, options)
		if err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, nil
}

func renderEntry(entry LogEntry, options Options) (string, error) {
	if options.NoToJSON {
		return renderPlainEntry(entry, options.AddSource), nil
	}

	payload := buildStructuredPayload(entry, options.AddSource)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal log entry: %w", err)
	}

	return string(encoded), nil
}

func renderPlainEntry(entry LogEntry, addSource bool) string {
	line := entry.originalLine()
	if !addSource {
		return line
	}

	return fmt.Sprintf("%s %s %s", entry.PodName, entry.ContainerName, line)
}

func renderYAMLOutput(entries []LogEntry, options Options) ([]byte, error) {
	items := make([]any, 0, len(entries))
	for _, entry := range entries {
		if options.NoToJSON {
			if options.AddSource {
				items = append(items, map[string]any{
					"line":             entry.originalLine(),
					"source_container": entry.ContainerName,
					"source_pod":       entry.PodName,
				})
				continue
			}
			items = append(items, entry.originalLine())
			continue
		}

		items = append(items, buildStructuredPayload(entry, options.AddSource))
	}

	if len(items) == 0 {
		return nil, nil
	}

	encoded, err := yaml.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("marshal yaml output: %w", err)
	}

	return encoded, nil
}

func buildStructuredPayload(entry LogEntry, addSource bool) map[string]any {
	payload := map[string]any{}
	if entry.Timestamp != "" {
		payload["kubernetes_timestamp"] = entry.Timestamp
	}
	if addSource {
		payload["source_container"] = entry.ContainerName
		payload["source_pod"] = entry.PodName
	}

	message := strings.TrimSpace(entry.messageText())
	if message == "" {
		payload["message"] = ""
	} else {
		var decoded any
		if err := json.Unmarshal([]byte(message), &decoded); err == nil {
			if object, ok := decoded.(map[string]any); ok {
				for key, value := range object {
					payload[key] = value
				}
			} else {
				payload["log"] = decoded
			}
		} else if mergeKlogPayload(payload, entry.Timestamp, message) {
		} else if mergeLogfmtPayload(payload, message) {
		} else if mergeAccessLogPayload(payload, message) {
		} else if mergeSquidPayload(payload, message) {
		} else {
			payload["message"] = entry.messageText()
		}
	}

	return payload
}

func (e LogEntry) originalLine() string {
	if e.Line != "" {
		return e.Line
	}
	if e.Timestamp == "" {
		return e.Message
	}
	if e.Message == "" {
		return e.Timestamp
	}

	return e.Timestamp + " " + e.Message
}

func (e LogEntry) messageText() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Line == "" {
		return ""
	}
	prefix := e.Timestamp + " "
	if e.Timestamp != "" && strings.HasPrefix(e.Line, prefix) {
		return strings.TrimPrefix(e.Line, prefix)
	}

	return e.Line
}

var klogPrefixPattern = regexp.MustCompile(`^([IWEF]\d{4})\s+(\d{2}:\d{2}:\d{2}\.\d+)\s+(\d+)\s+([^\]]+)\]\s*(.*)$`)
var accessLogPattern = regexp.MustCompile(`^(\S+) (\S+) (\S+) \[([^\]]+)\] "([A-Z]+) ([^"]*?) ([^"]+)" (\d{3}) (\d+|-) "([^"]*)" "([^"]*)" "([^"]*)"$`)
var squidLogPattern = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2})\|\s*(.*)$`)

func mergeKlogPayload(payload map[string]any, kubernetesTimestamp, message string) bool {
	matches := klogPrefixPattern.FindStringSubmatch(message)
	if matches == nil {
		return false
	}

	payload["level"] = matches[1]
	if logTimestamp, ok := buildKlogTimestamp(kubernetesTimestamp, matches[1], matches[2]); ok {
		payload["log_timestamp"] = logTimestamp
	} else {
		payload["log_timestamp"] = matches[2]
	}
	if threadID, err := strconv.Atoi(matches[3]); err == nil {
		payload["thread_id"] = threadID
	} else {
		payload["thread_id"] = matches[3]
	}
	payload["caller"] = matches[4]

	parsedMessage, values, ok := parseKlogBody(matches[5])
	if !ok {
		payload["message"] = message
		return true
	}

	if parsedMessage != "" {
		payload["message"] = parsedMessage
	}
	for key, value := range values {
		payload[key] = value
	}

	return true
}

func mergeLogfmtPayload(payload map[string]any, message string) bool {
	values, ok := parseLogfmt(message)
	if !ok || len(values) == 0 {
		return false
	}

	for key, value := range values {
		switch key {
		case "msg":
			payload["message"] = value
		case "time":
			payload["log_timestamp"] = value
		default:
			payload[key] = value
		}
	}

	if _, ok := payload["message"]; !ok {
		if value, ok := values["message"]; ok {
			payload["message"] = value
		}
	}

	return true
}

func mergeAccessLogPayload(payload map[string]any, message string) bool {
	matches := accessLogPattern.FindStringSubmatch(message)
	if matches == nil {
		return false
	}

	payload["remote_addr"] = matches[1]
	payload["remote_logname"] = nilIfDash(matches[2])
	payload["remote_user"] = nilIfDash(matches[3])
	payload["log_timestamp"] = matches[4]
	payload["method"] = matches[5]
	payload["request_uri"] = matches[6]
	payload["protocol"] = matches[7]
	if status, err := strconv.Atoi(matches[8]); err == nil {
		payload["status"] = status
	} else {
		payload["status"] = matches[8]
	}
	if matches[9] == "-" {
		payload["body_bytes_sent"] = nil
	} else if bytesSent, err := strconv.Atoi(matches[9]); err == nil {
		payload["body_bytes_sent"] = bytesSent
	} else {
		payload["body_bytes_sent"] = matches[9]
	}
	payload["referer"] = nilIfDash(matches[10])
	payload["user_agent"] = nilIfDash(matches[11])
	payload["forwarded_for"] = nilIfDash(matches[12])
	payload["message"] = fmt.Sprintf("%s %s", matches[5], matches[6])

	return true
}

func mergeSquidPayload(payload map[string]any, message string) bool {
	matches := squidLogPattern.FindStringSubmatch(message)
	if matches == nil {
		return false
	}

	payload["log_timestamp"] = matches[1]
	payload["message"] = matches[2]

	return true
}

func buildKlogTimestamp(kubernetesTimestamp, level, clock string) (string, bool) {
	if len(level) != 5 {
		return "", false
	}

	if kubernetesTimestamp == "" {
		return level[1:3] + "-" + level[3:5] + "T" + clock, true
	}

	baseTime, err := time.Parse(time.RFC3339Nano, kubernetesTimestamp)
	if err != nil {
		return "", false
	}

	month, err := strconv.Atoi(level[1:3])
	if err != nil {
		return "", false
	}
	day, err := strconv.Atoi(level[3:5])
	if err != nil {
		return "", false
	}

	parsedClock, err := time.Parse("15:04:05.999999", clock)
	if err != nil {
		return "", false
	}

	logTime := time.Date(
		baseTime.Year(),
		time.Month(month),
		day,
		parsedClock.Hour(),
		parsedClock.Minute(),
		parsedClock.Second(),
		parsedClock.Nanosecond(),
		baseTime.Location(),
	)

	return logTime.Format(time.RFC3339Nano), true
}

func parseKlogBody(body string) (string, map[string]any, bool) {
	rest := strings.TrimSpace(body)
	if rest == "" {
		return "", nil, true
	}

	values := make(map[string]any)
	message := ""
	position := 0

	if rest[0] == '"' {
		parsed, next, ok := readQuotedToken(rest, 0)
		if !ok {
			return "", nil, false
		}
		message = parsed
		position = next
	}

	for position < len(rest) {
		for position < len(rest) && rest[position] == ' ' {
			position++
		}
		if position >= len(rest) {
			break
		}

		equalIndex := strings.IndexByte(rest[position:], '=')
		if equalIndex == -1 {
			return "", nil, false
		}
		equalIndex += position
		key := strings.TrimSpace(rest[position:equalIndex])
		if key == "" {
			return "", nil, false
		}

		position = equalIndex + 1
		value, next, ok := readKlogValue(rest, position)
		if !ok {
			return "", nil, false
		}
		values[key] = value
		position = next
	}

	return message, values, true
}

func readKlogValue(input string, start int) (any, int, bool) {
	if start >= len(input) {
		return nil, start, false
	}

	if input[start] == '"' {
		value, next, ok := readQuotedToken(input, start)
		if !ok {
			return nil, start, false
		}
		return value, next, true
	}

	end := start
	for end < len(input) && input[end] != ' ' {
		end++
	}
	raw := input[start:end]
	if integerValue, err := strconv.Atoi(raw); err == nil {
		return integerValue, end, true
	}

	return raw, end, true
}

func readQuotedToken(input string, start int) (string, int, bool) {
	if start >= len(input) || input[start] != '"' {
		return "", start, false
	}

	end := start + 1
	escaped := false
	for end < len(input) {
		switch {
		case escaped:
			escaped = false
		case input[end] == '\\':
			escaped = true
		case input[end] == '"':
			unquoted, err := strconv.Unquote(input[start : end+1])
			if err != nil {
				return "", start, false
			}
			return unquoted, end + 1, true
		}
		end++
	}

	return "", start, false
}

func parseLogfmt(input string) (map[string]any, bool) {
	values := make(map[string]any)
	position := 0
	for position < len(input) {
		for position < len(input) && input[position] == ' ' {
			position++
		}
		if position >= len(input) {
			break
		}

		equalIndex := strings.IndexByte(input[position:], '=')
		if equalIndex == -1 {
			return nil, false
		}
		equalIndex += position

		key := strings.TrimSpace(input[position:equalIndex])
		if key == "" {
			return nil, false
		}

		value, next, ok := readKlogValue(input, equalIndex+1)
		if !ok {
			return nil, false
		}

		values[key] = value
		position = next
	}

	return values, len(values) > 0
}

func nilIfDash(value string) any {
	if value == "-" {
		return nil
	}

	return value
}
