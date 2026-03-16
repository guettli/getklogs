package getklogs

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type structuredPayload struct {
	KubernetesTimestamp string
	SourceContainer     string
	SourcePod           string
	Message             any
	HasMessage          bool
	Log                 any
	HasLog              bool
	Extra               map[string]any
}

func newStructuredPayload(entry LogEntry, meta bool) structuredPayload {
	payload := structuredPayload{
		KubernetesTimestamp: entry.Timestamp,
	}
	if meta {
		payload.SourceContainer = entry.ContainerName
		payload.SourcePod = entry.PodName
	}

	return payload
}

func (p *structuredPayload) setMessage(message any) {
	p.Message = message
	p.HasMessage = true
}

func (p *structuredPayload) setLog(log any) {
	p.Log = log
	p.HasLog = true
}

func (p *structuredPayload) setExtra(key string, value any) {
	if p.Extra == nil {
		p.Extra = make(map[string]any)
	}
	p.Extra[key] = value
}

func (p structuredPayload) asMap() map[string]any {
	size := len(p.Extra)
	if p.KubernetesTimestamp != "" {
		size++
	}
	if p.SourceContainer != "" {
		size++
	}
	if p.SourcePod != "" {
		size++
	}
	if p.HasMessage {
		size++
	}
	if p.HasLog {
		size++
	}

	payload := make(map[string]any, size)
	if p.KubernetesTimestamp != "" {
		payload["kubernetes_timestamp"] = p.KubernetesTimestamp
	}
	if p.SourceContainer != "" {
		payload["source_container"] = p.SourceContainer
	}
	if p.SourcePod != "" {
		payload["source_pod"] = p.SourcePod
	}
	if p.HasMessage {
		payload["message"] = p.Message
	}
	if p.HasLog {
		payload["log"] = p.Log
	}
	for key, value := range p.Extra {
		payload[key] = value
	}

	return payload
}

func renderOutput(entries []LogEntry, options Options) ([]byte, error) {
	switch options.Output {
	case OutputFormatJSON, OutputFormatRaw:
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
	if options.Output == OutputFormatRaw {
		return renderPlainEntry(entry, options.Meta), nil
	}
	if options.Output == OutputFormatYAML {
		return renderYAMLEntry(entry, options.Meta)
	}

	payload := buildStructuredPayload(entry, options.Meta)
	encoded, err := json.Marshal(payload.asMap())
	if err != nil {
		return "", fmt.Errorf("marshal log entry: %w", err)
	}

	return string(encoded), nil
}

func renderYAMLEntry(entry LogEntry, meta bool) (string, error) {
	payload := buildStructuredPayload(entry, meta)
	encoded, err := yaml.Marshal(payload.asMap())
	if err != nil {
		return "", fmt.Errorf("marshal yaml log entry: %w", err)
	}

	return strings.TrimSuffix(string(encoded), "\n"), nil
}

func renderPlainEntry(entry LogEntry, meta bool) string {
	line := entry.rawLine()
	if !meta {
		return line
	}

	return fmt.Sprintf("%s %s %s", entry.PodName, entry.ContainerName, line)
}

func renderYAMLOutput(entries []LogEntry, options Options) ([]byte, error) {
	items := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		items = append(items, buildStructuredPayload(entry, options.Meta).asMap())
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

func buildStructuredPayload(entry LogEntry, meta bool) structuredPayload {
	payload := newStructuredPayload(entry, meta)

	message := strings.TrimSpace(entry.messageText())
	if message == "" {
		payload.setMessage("")
	} else {
		var decoded any
		if err := json.Unmarshal([]byte(message), &decoded); err == nil {
			if object, ok := decoded.(map[string]any); ok {
				for key, value := range object {
					payload.setExtra(key, value)
				}
			} else {
				payload.setLog(decoded)
			}
		} else if mergeKlogPayload(&payload, entry.Timestamp, message) {
		} else if mergeLogfmtPayload(&payload, message) {
		} else if mergeAccessLogPayload(&payload, message) {
		} else if mergeSquidPayload(&payload, message) {
		} else {
			payload.setMessage(entry.messageText())
		}
	}

	return payload
}

func (e LogEntry) rawLine() string {
	if e.Line != "" {
		return e.Line
	}
	if e.Message != "" {
		return e.Message
	}

	return e.Timestamp
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

func mergeKlogPayload(payload *structuredPayload, kubernetesTimestamp, message string) bool {
	matches := klogPrefixPattern.FindStringSubmatch(message)
	if matches == nil {
		return false
	}

	payload.setExtra("level", klogSeverity(matches[1]))
	if logTimestamp, ok := buildKlogTimestamp(kubernetesTimestamp, matches[1], matches[2]); ok {
		payload.setExtra("log_timestamp", logTimestamp)
	} else {
		payload.setExtra("log_timestamp", matches[2])
	}
	if threadID, err := strconv.Atoi(matches[3]); err == nil {
		payload.setExtra("thread_id", threadID)
	} else {
		payload.setExtra("thread_id", matches[3])
	}
	payload.setExtra("caller", matches[4])

	parsedMessage, values, ok := parseKlogBody(matches[5])
	if !ok {
		payload.setMessage(message)
		return true
	}

	if parsedMessage != "" {
		payload.setMessage(parsedMessage)
	}
	for key, value := range values {
		payload.setExtra(key, value)
	}

	return true
}

func klogSeverity(level string) string {
	if len(level) == 0 {
		return level
	}

	switch level[0] {
	case 'I':
		return "INFO"
	case 'W':
		return "WARN"
	case 'E':
		return "ERROR"
	case 'F':
		return "FATAL"
	default:
		return level
	}
}

func mergeLogfmtPayload(payload *structuredPayload, message string) bool {
	values, ok := parseLogfmt(message)
	if !ok || len(values) == 0 {
		return false
	}

	for key, value := range values {
		switch key {
		case "msg":
			payload.setMessage(value)
		case "time":
			payload.setExtra("log_timestamp", value)
		default:
			payload.setExtra(key, value)
		}
	}

	if !payload.HasMessage {
		if value, ok := values["message"]; ok {
			payload.setMessage(value)
		}
	}

	return true
}

func mergeAccessLogPayload(payload *structuredPayload, message string) bool {
	matches := accessLogPattern.FindStringSubmatch(message)
	if matches == nil {
		return false
	}

	payload.setExtra("remote_addr", matches[1])
	payload.setExtra("remote_logname", nilIfDash(matches[2]))
	payload.setExtra("remote_user", nilIfDash(matches[3]))
	payload.setExtra("log_timestamp", matches[4])
	payload.setExtra("method", matches[5])
	payload.setExtra("request_uri", matches[6])
	payload.setExtra("protocol", matches[7])
	if status, err := strconv.Atoi(matches[8]); err == nil {
		payload.setExtra("status", status)
	} else {
		payload.setExtra("status", matches[8])
	}
	if matches[9] == "-" {
		payload.setExtra("body_bytes_sent", nil)
	} else if bytesSent, err := strconv.Atoi(matches[9]); err == nil {
		payload.setExtra("body_bytes_sent", bytesSent)
	} else {
		payload.setExtra("body_bytes_sent", matches[9])
	}
	payload.setExtra("referer", nilIfDash(matches[10]))
	payload.setExtra("user_agent", nilIfDash(matches[11]))
	payload.setExtra("forwarded_for", nilIfDash(matches[12]))
	payload.setMessage(fmt.Sprintf("%s %s", matches[5], matches[6]))

	return true
}

func mergeSquidPayload(payload *structuredPayload, message string) bool {
	matches := squidLogPattern.FindStringSubmatch(message)
	if matches == nil {
		return false
	}

	payload.setExtra("log_timestamp", matches[1])
	payload.setMessage(matches[2])

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
