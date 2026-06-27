package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

type Config struct {
	ListenAddr    string
	BackendURL    string
	AddToTotals   bool
	Tokenizer     TokenizerConfig
}

func loadConfig() Config {
	cfg := Config{
		ListenAddr:  getenv("PROXY_LISTEN", ":8080"),
		BackendURL:  getenv("VLLM_URL", "http://localhost:8000"),
		AddToTotals: getenvBool("ADD_TO_TOTALS", false),
		Tokenizer: TokenizerConfig{
			Type: TokenizerType(getenv("TOKENIZER_TYPE", "tiktoken")),
			Path: getenv("TOKENIZER_PATH", "cl100k_base"),
		},
	}
	return cfg
}

func main() {
	cfg := loadConfig()
	if cfg.BackendURL == "" {
		log.Fatal("VLLM_URL is required")
	}

	counter := NewCounter(cfg.Tokenizer)
	log.Printf("tokenizer: type=%s path=%s", cfg.Tokenizer.Type, cfg.Tokenizer.Path)

	proxy := NewProxy(cfg, counter)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.Handle("/", proxy)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("reasoning-proxy listening on %s -> %s", cfg.ListenAddr, cfg.BackendURL)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
