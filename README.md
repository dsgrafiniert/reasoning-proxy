# reasoning-proxy

Go reverse proxy that injects `completion_tokens_details.reasoning_tokens` into vLLM chat completion responses. Sits between vLLM and Bifrost on the same host.

```
Client → Bifrost → reasoning-proxy → vLLM
                      ↓
              intercepts responses
              counts reasoning tokens
              injects into usage
```

## Why

vLLM generates `reasoning` (formerly `reasoning_content`) in chat responses but does not include `reasoning_tokens` in the `usage` field. Bifrost's governance/billing relies on this field for cost attribution. This proxy fills the gap without modifying Bifrost or vLLM.

## Quick start

```bash
# Point at your vLLM backend
export VLLM_URL=http://localhost:8000
go run .

# Bifrost connects to the proxy instead of vLLM directly
# vllm_key_config.url = http://localhost:8080
```

## Configuration

| Env var | Default | Description |
|---------|---------|-------------|
| `PROXY_LISTEN` | `:8080` | Listen address |
| `VLLM_URL` | `http://localhost:8000` | vLLM backend URL |
| `CHARS_PER_TOKEN` | `3.5` | Heuristic: rune count / this = token count |
| `ADD_TO_TOTALS` | `false` | If `true`, add reasoning tokens to `completion_tokens` and `total_tokens` (matches Bifrost reasoningmeter behavior). Default follows OpenAI spec: `reasoning_tokens` is a subset of `completion_tokens`. |

## How it works

### Non-streaming
1. Forwards request to vLLM
2. Reads full response body
3. Extracts `reasoning` / `reasoning_content` from `choices[].message`
4. Counts tokens via heuristic (chars / 3.5)
5. Injects `usage.completion_tokens_details.reasoning_tokens`
6. Returns modified response

### Streaming (SSE)
1. Injects `stream_options.include_usage: true` into the request (if not already set)
2. Passes through all chunks immediately — no buffering
3. Byte-scans each chunk for `"reasoning"` — parses only matching chunks to accumulate text
4. On the final chunk containing `"usage"`, injects `reasoning_tokens`
5. All other chunks pass through untouched

Only `/v1/chat/completions` POST is intercepted. All other endpoints use `httputil.ReverseProxy` pass-through with zero overhead.

## Latency

On localhost (same pod / same node in OpenShift):

| Path | Overhead |
|------|----------|
| Non-chat endpoints | ~0 (pure reverse proxy) |
| Non-streaming chat | ~0.5ms (JSON round-trip) |
| Streaming chat (per chunk) | ~0.01ms (byte scan only) |
| Streaming chat (usage chunk) | ~0.1ms (JSON parse + modify) |

Connection pooling: 10,000 idle conns per host, 120s idle timeout, keep-alive reused.

## OpenShift deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: reasoning-proxy
spec:
  replicas: 1
  selector:
    matchLabels:
      app: reasoning-proxy
  template:
    spec:
      containers:
      - name: reasoning-proxy
        image: reasoning-proxy:latest
        ports:
        - containerPort: 8080
        env:
        - name: VLLM_URL
          value: "http://vllm.default.svc.cluster.local:8000"
        - name: CHARS_PER_TOKEN
          value: "3.5"
        readinessProbe:
          httpGet:
            path: /healthz
            port: 8080
          initialDelaySeconds: 2
        livenessProbe:
          httpGet:
            path: /healthz
            port: 8080
          initialDelaySeconds: 5
        securityContext:
          runAsNonRoot: true
          runAsUser: 65532
```

Point Bifrost's vLLM key at the proxy service instead of vLLM directly:

```json
{
  "vllm_key_config": {
    "url": "http://reasoning-proxy.default.svc.cluster.local:8080"
  }
}
```

## Build

```bash
docker build -t reasoning-proxy .
# or
go build -ldflags="-s -w" -trimpath -o reasoning-proxy .
```

Binary is ~5MB, statically linked, no CGO, runs on distroless.
