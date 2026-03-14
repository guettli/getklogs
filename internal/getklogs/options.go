package getklogs

import (
	"strings"
	"time"
)

const DefaultSince = 3 * time.Hour

const (
	OutputFormatJSON = "json"
	OutputFormatYAML = "yaml"
)

type Options struct {
	Namespace string
	Since     time.Duration
	TermQuery string

	OutputFile string
	AddSource  bool
	NoToJSON   bool
	Output     string
}

func NormalizeOptions(options Options) Options {
	if strings.HasSuffix(options.TermQuery, ".log") {
		options.OutputFile = options.TermQuery
		options.TermQuery = strings.TrimSuffix(options.TermQuery, ".log")
	}
	if options.Output == "" {
		options.Output = OutputFormatJSON
	}
	return options
}
