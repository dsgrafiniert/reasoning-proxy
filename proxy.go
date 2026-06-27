package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Proxy struct {
	cfg          Config
	backendURL   *url.URL
	client       *http.Client
	reverseProxy *httputil.ReverseProxy
	counter      Counter
}

func NewProxy(cfg Config, counter Counter) *Proxy {
	backendURL, err := url.Parse(cfg.BackendURL)
	if err != nil {
		log.Fatalf("invalid VLLM_URL: %v", err)
	}

	transport := &http.Transport{
		MaxIdleConns:        10000,
		MaxIdleConnsPerHost: 10000,
		IdleConnTimeout:     120 * time.Second,
		DisableCompression:  true,
	}

	rp := httputil.NewSingleHostReverseProxy(backendURL)
	rp.Transport = transport

	return &Proxy{
		cfg:          cfg,
		backendURL:   backendURL,
		client:       &http.Client{Transport: transport},
		reverseProxy: rp,
		counter:      counter,
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/chat/completions" && r.Method == http.MethodPost {
		p.handleChatCompletion(w, r)
		return
	}
	p.reverseProxy.ServeHTTP(w, r)
}

func (p *Proxy) handleChatCompletion(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	r.Body.Close()

	body = ensureIncludeUsage(body)

	target := p.backendURL.JoinPath(r.URL.Path)
	if r.URL.RawQuery != "" {
		target.RawQuery = r.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "failed to create request", http.StatusInternalServerError)
		return
	}

	copyHeaders(req.Header, r.Header)
	req.Header.Del("Accept-Encoding")
	req.ContentLength = int64(len(body))

	resp, err := p.client.Do(req)
	if err != nil {
		http.Error(w, "failed to forward request", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if isStreamingResponse(resp) {
		p.handleStreaming(w, resp)
	} else {
		p.handleNonStreaming(w, resp)
	}
}

func (p *Proxy) handleNonStreaming(w http.ResponseWriter, resp *http.Response) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		copyHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
		return
	}

	modified, ok := p.modifyNonStreamingResponse(body)

	copyHeaders(w.Header(), resp.Header)
	w.Header().Del("Transfer-Encoding")

	if ok {
		w.Header().Set("Content-Length", strconv.Itoa(len(modified)))
		w.WriteHeader(resp.StatusCode)
		w.Write(modified)
	} else {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(resp.StatusCode)
		w.Write(body)
	}
}

func (p *Proxy) modifyNonStreamingResponse(body []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var resp map[string]any
	if err := dec.Decode(&resp); err != nil {
		return body, false
	}

	reasoningText := extractReasoningFromChoices(resp)
	if reasoningText == "" {
		return body, false
	}

	count := p.counter.Count(reasoningText)
	if count == 0 {
		return body, false
	}

	usage, ok := resp["usage"].(map[string]any)
	if !ok {
		return body, false
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

	modified, err := json.Marshal(resp)
	if err != nil {
		return body, false
	}
	return modified, true
}

func extractReasoningFromChoices(resp map[string]any) string {
	choices, ok := resp["choices"].([]any)
	if !ok {
		return ""
	}
	var sb strings.Builder
	for _, c := range choices {
		choice, ok := c.(map[string]any)
		if !ok {
			continue
		}
		msg, ok := choice["message"].(map[string]any)
		if !ok {
			continue
		}
		if r, ok := msg["reasoning"].(string); ok && r != "" {
			sb.WriteString(r)
		}
		if rc, ok := msg["reasoning_content"].(string); ok && rc != "" {
			sb.WriteString(rc)
		}
	}
	return sb.String()
}

func ensureIncludeUsage(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"stream"`)) {
		return body
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var req map[string]any
	if err := dec.Decode(&req); err != nil {
		return body
	}

	stream, ok := req["stream"].(bool)
	if !ok || !stream {
		return body
	}

	so, ok := req["stream_options"].(map[string]any)
	if ok {
		if iu, ok := so["include_usage"].(bool); ok && iu {
			return body
		}
	} else {
		so = map[string]any{}
	}

	so["include_usage"] = true
	req["stream_options"] = so

	modified, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return modified
}

func isStreamingResponse(resp *http.Response) bool {
	ct := resp.Header.Get("Content-Type")
	return strings.Contains(ct, "text/event-stream")
}

func copyHeaders(dst, src http.Header) {
	for k, v := range src {
		dst[k] = v
	}
}

func addToInt(m map[string]any, key string, delta int) {
	if n, ok := m[key].(json.Number); ok {
		if i, err := n.Int64(); err == nil {
			m[key] = json.Number(strconv.FormatInt(i+int64(delta), 10))
		}
	}
}
