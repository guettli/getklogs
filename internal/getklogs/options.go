package getklogs

import "time"

const DefaultSince = 3 * time.Hour

const (
	OutputFormatJSON = "json"
	OutputFormatYAML = "yaml"
)

type Options struct {
	Namespace string
	Since     time.Duration
	TermQuery string

	Pod       bool
	All       bool
	Stdout    bool
	AddSource bool
	NoToJSON  bool
	TailLines int
	Output    string
}

func NormalizeOptions(options Options) Options {
	if options.Output == "" {
		options.Output = OutputFormatJSON
	}
	return options
}
