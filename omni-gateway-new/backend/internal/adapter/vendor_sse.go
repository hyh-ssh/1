package adapter

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// parseNamedSSE preserves the upstream event name for native provider streams.
// It follows the same framing rules as parseSSE but leaves payload decoding to
// the provider-specific adapter.
func parseNamedSSE(r io.Reader, emit func(name string, data []byte) error) error {
	if r == nil {
		return fmt.Errorf("nil SSE response body")
	}
	if emit == nil {
		return fmt.Errorf("nil SSE event callback")
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, initialSSEBufferSize), maxSSELineSize)
	var dataLines []string
	var eventName string
	frameBytes := 0
	dispatch := func() error {
		frameBytes = 0
		if len(dataLines) == 0 {
			eventName = ""
			return nil
		}
		data := []byte(strings.Join(dataLines, "\n"))
		name := eventName
		dataLines = nil
		eventName = ""
		return emit(name, data)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		frameBytes += len(line) + 1
		if frameBytes > maxSSELineSize {
			return fmt.Errorf("upstream SSE frame exceeds size limit")
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value := line, ""
		if colon := strings.IndexByte(line, ':'); colon >= 0 {
			field, value = line[:colon], line[colon+1:]
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
		}
		switch field {
		case "event":
			eventName = value
		case "data":
			dataLines = append(dataLines, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read SSE stream: %w", err)
	}
	return dispatch()
}
