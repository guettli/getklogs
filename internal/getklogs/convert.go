package getklogs

import (
	"fmt"
	"io"
	"strings"
	"time"
)

func ConvertInput(reader io.Reader, writer io.Writer, options Options) error {
	options = NormalizeOptions(options)

	entries, err := readInputEntries(reader)
	if err != nil {
		return err
	}

	content, err := renderOutput(entries, options)
	if err != nil {
		return err
	}
	if len(content) == 0 {
		return nil
	}

	_, err = writer.Write(content)
	return err
}

func readInputEntries(reader io.Reader) ([]LogEntry, error) {
	scanner := newLineScanner(reader)
	var entries []LogEntry
	for scanner.Scan() {
		entries = append(entries, parseInputEntry(scanner.Text()))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
	}

	return entries, nil
}

func parseInputEntry(line string) LogEntry {
	timestamp, message, ok := splitStructuredTimestamp(line)
	if !ok {
		return LogEntry{
			Line:    line,
			Message: line,
		}
	}

	return LogEntry{
		Timestamp: timestamp,
		Line:      line,
		Message:   message,
	}
}

func splitStructuredTimestamp(line string) (string, string, bool) {
	if _, err := time.Parse(time.RFC3339Nano, line); err == nil {
		return line, "", true
	}

	timestamp, message, found := strings.Cut(line, " ")
	if !found {
		return "", "", false
	}
	if _, err := time.Parse(time.RFC3339Nano, timestamp); err != nil {
		return "", "", false
	}

	return timestamp, message, true
}
