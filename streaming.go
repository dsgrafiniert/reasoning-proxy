package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func (p *Proxy) handleStreaming(w http.ResponseWriter, resp *http.Response) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		copyHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	flusher.Flush()

	reader := bufio.NewReaderSize(resp.Body, 64*1024)
	var reasoningBuf strings.Builder

	for {
		line, err := reader.ReadString('\n')
		eof := err == io.EOF

		if line != "" {
			processed := p.processSSELine([]byte(line), &reasoningBuf)
			w.Write(processed)
			flusher.Flush()
		}

		if eof || err != nil {
			break
		}
	}
}

func (p *Proxy) processSSELine(line []byte, reasoningBuf *strings.Builder) []byte {
	trimmed := bytes.TrimSpace(line)
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return line
	}

	idx := bytes.IndexByte(trimmed, '{')
	if idx == -1 {
		return line
	}

	data := trimmed[idx:]

	if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		return line
	}

	hasReasoning := bytes.Contains(data, []byte(`"reasoning"`)) ||
		bytes.Contains(data, []byte(`"reasoning_content"`))
	hasUsage := bytes.Contains(data, []byte(`"usage"`))

	if hasReasoning && !hasUsage {
		extractReasoningFromDelta(data, reasoningBuf)
		return line
	}

	if hasUsage && reasoningBuf.Len() > 0 {
		return p.modifyStreamUsage(line, data, reasoningBuf.String())
	}

	return line
}

func extractReasoningFromDelta(data []byte, buf *strings.Builder) {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Reasoning        *string `json:"reasoning"`
				ReasoningContent *string `json:"reasoning_content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &chunk); err != nil {
		return
	}
	for _, c := range chunk.Choices {
		if c.Delta.Reasoning != nil && *c.Delta.Reasoning != "" {
			buf.WriteString(*c.Delta.Reasoning)
		}
		if c.Delta.ReasoningContent != nil && *c.Delta.ReasoningContent != "" {
			buf.WriteString(*c.Delta.ReasoningContent)
		}
	}
}

func (p *Proxy) modifyStreamUsage(line, data []byte, reasoningText string) []byte {
	count := p.counter.Count(reasoningText)
	if count == 0 {
		return line
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var chunk map[string]any
	if err := dec.Decode(&chunk); err != nil {
		return line
	}

	usage, ok := chunk["usage"].(map[string]any)
	if !ok {
		return line
	}

	details, ok := usage["completion_tokens_details"].(map[string]any)
	if !ok {
		details = map[string]any{}
	}
	details["reasoning_tokens"] = count
	usage["completion_tokens_details"] = details

	if p.cfg.AddToTotals {
		addToInt(usage, "completion_tokens", count)
		addToInt(usage, "total_tokens", count)
	}

	modified, err := json.Marshal(chunk)
	if err != nil {
		return line
	}

	prefix := line[:bytes.IndexByte(line, '{')]
	result := make([]byte, 0, len(prefix)+len(modified)+1)
	result = append(result, prefix...)
	result = append(result, modified...)
	result = append(result, '\n')
	return result
}
