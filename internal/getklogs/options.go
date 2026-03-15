package getklogs

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const DefaultSince = 3 * time.Hour

const (
	OutputFormatJSON = "json"
	OutputFormatRaw  = "raw"
	OutputFormatYAML = "yaml"
)

type Options struct {
	Namespace  string
	Since      time.Duration
	TermQuery  string
	OutDir     string
	Kubeconfig string
	Node       string

	Pod          bool
	All          bool
	Stdout       bool
	Meta         bool
	PerContainer bool
	Follow       bool
	TailLines    int
	Output       string
}

func NormalizeOptions(options Options) Options {
	options.Output = strings.ToLower(strings.TrimSpace(options.Output))
	if options.Output == "" {
		options.Output = OutputFormatJSON
	}
	if options.Follow {
		options.Stdout = true
	}
	options.OutDir = strings.TrimSpace(options.OutDir)
	if options.OutDir == "" {
		options.OutDir = "."
	}
	options.OutDir = filepath.Clean(options.OutDir)
	options.Kubeconfig = strings.TrimSpace(options.Kubeconfig)
	options.Node = strings.TrimSpace(options.Node)
	return options
}

func ValidateOptions(options Options) error {
	options = NormalizeOptions(options)

	switch options.Output {
	case OutputFormatJSON, OutputFormatRaw, OutputFormatYAML:
	default:
		return fmt.Errorf("unsupported output format %q", options.Output)
	}

	if options.TailLines < 0 {
		return errors.New("tail must be zero or greater")
	}
	if options.Stdout && options.OutDir != "." {
		return errors.New("--outdir cannot be used with --stdout")
	}
	if options.Stdout && options.PerContainer {
		return errors.New("--per-container cannot be used with --stdout")
	}
	if options.Follow && options.TailLines > 0 {
		return errors.New("--follow cannot be used with --tail")
	}
	if options.Node != "" {
		if _, err := path.Match(options.Node, ""); err != nil {
			return fmt.Errorf("invalid --node pattern %q: %w", options.Node, err)
		}
	}

	return nil
}

func DescribeSinceWindow(since time.Duration) string {
	if since <= 0 {
		return "all available logs"
	}

	return fmt.Sprintf("last %s", formatDurationCompact(since))
}

func formatDurationCompact(duration time.Duration) string {
	formatted := duration.String()
	for _, suffix := range []string{"0s", "0m"} {
		formatted = strings.TrimSuffix(formatted, suffix)
	}

	return formatted
}
