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
