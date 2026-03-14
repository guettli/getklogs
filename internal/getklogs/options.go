package getklogs

import (
	"strings"
	"time"
)

const DefaultSince = 3 * time.Hour

type Options struct {
	Namespace  string
	Since      time.Duration
	TermQuery  string
	OutputFile string
}

func NormalizeOptions(options Options) Options {
	if strings.HasSuffix(options.TermQuery, ".log") {
		options.OutputFile = options.TermQuery
		options.TermQuery = strings.TrimSuffix(options.TermQuery, ".log")
	}
	return options
}
