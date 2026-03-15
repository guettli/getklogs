package getklogs

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/manifoldco/promptui"
	xterm "golang.org/x/term"
)

func (a App) listTargets(ctx context.Context, options Options) ([]Workload, error) {
	if options.Pod {
		return a.Cluster.ListPods(ctx, options.Namespace, options.Node)
	}

	workloads, err := a.Cluster.ListWorkloads(ctx, options.Namespace, options.Node)
	if err != nil {
		return nil, err
	}
	if !options.All {
		return workloads, nil
	}

	standalonePods, err := a.Cluster.ListStandalonePods(ctx, options.Namespace, options.Node)
	if err != nil {
		return nil, err
	}

	return append(workloads, standalonePods...), nil
}

func noTargetsFoundError(options Options) error {
	nodeQualifier := ""
	if options.Node != "" {
		nodeQualifier = fmt.Sprintf(" on nodes matching %q", options.Node)
	}

	switch {
	case options.Pod && options.Namespace != "":
		return fmt.Errorf("no pods found in namespace %q%s", options.Namespace, nodeQualifier)
	case options.Pod:
		return fmt.Errorf("no pods found in any namespace%s", nodeQualifier)
	case options.All && options.Namespace != "":
		return fmt.Errorf("no Deployment/DaemonSet/StatefulSet or standalone pod found in namespace %q%s", options.Namespace, nodeQualifier)
	case options.All:
		return fmt.Errorf("no Deployment/DaemonSet/StatefulSet or standalone pod found in any namespace%s", nodeQualifier)
	case options.Namespace != "":
		return fmt.Errorf("no Deployment/DaemonSet/StatefulSet found in namespace %q%s", options.Namespace, nodeQualifier)
	default:
		return fmt.Errorf("no Deployment/DaemonSet/StatefulSet found in any namespace%s", nodeQualifier)
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
			if errors.Is(err, io.EOF) && len(line) > 0 {
				selection, convErr := strconv.Atoi(strings.TrimSpace(line))
				if convErr == nil && selection >= 1 && selection <= len(workloads) {
					return workloads[selection-1], nil
				}
			}
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

func matchWorkloadSearch(input string, workload Workload) bool {
	return workloadMatchesSearch(input, workload)
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
	var matches []Workload
	for _, workload := range workloads {
		if workloadMatchesSearch(term, workload) {
			matches = append(matches, workload)
		}
	}
	return matches
}

func workloadMatchesSearch(term string, workload Workload) bool {
	needles := strings.Fields(normalizeSearchText(term))
	if len(needles) == 0 {
		return true
	}

	haystack := searchableWorkloadText(workload)
	start := 0
	for _, needle := range needles {
		index := strings.Index(haystack[start:], needle)
		if index < 0 {
			return false
		}
		start += index + len(needle)
	}

	return true
}

func searchableWorkloadText(workload Workload) string {
	return normalizeSearchText(strings.Join([]string{
		workload.Name,
		workload.Namespace,
		workload.Kind,
		workload.ReadyText(),
	}, " "))
}

func normalizeSearchText(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), " ")
}
