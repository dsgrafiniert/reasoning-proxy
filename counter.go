package main

import (
	"fmt"
	"log"
	"sync"

	"github.com/eliben/go-sentencepiece"
	"github.com/pkoukk/tiktoken-go"
)

type Counter interface {
	Count(text string) int
}

type TokenizerType string

const (
	TokenizerTiktoken      TokenizerType = "tiktoken"
	TokenizerSentencepiece TokenizerType = "sentencepiece"
)

type TokenizerConfig struct {
	Type TokenizerType
	Path string
}

func NewCounter(cfg TokenizerConfig) Counter {
	switch cfg.Type {
	case TokenizerTiktoken:
		enc, err := tiktoken.GetEncoding(cfg.Path)
		if err != nil {
			log.Fatalf("failed to load tiktoken encoding %q: %v", cfg.Path, err)
		}
		return &TiktokenCounter{encoder: enc}

	case TokenizerSentencepiece:
		proc, err := globalSPCache.get(cfg.Path)
		if err != nil {
			log.Fatalf("failed to load sentencepiece model from %q: %v", cfg.Path, err)
		}
		return &SentencepieceCounter{processor: proc}

	default:
		log.Fatalf("unsupported tokenizer type: %s", cfg.Type)
		return nil
	}
}

type TiktokenCounter struct {
	encoder *tiktoken.Tiktoken
}

func (c *TiktokenCounter) Count(text string) int {
	if text == "" {
		return 0
	}
	return len(c.encoder.EncodeOrdinary(text))
}

type SentencepieceCounter struct {
	processor *sentencepiece.Processor
}

func (c *SentencepieceCounter) Count(text string) int {
	if text == "" {
		return 0
	}
	return len(c.processor.Encode(text))
}

type sentencepieceCache struct {
	mu         sync.Mutex
	processors map[string]*sentencepiece.Processor
}

var globalSPCache = &sentencepieceCache{
	processors: make(map[string]*sentencepiece.Processor),
}

func (c *sentencepieceCache) get(modelPath string) (*sentencepiece.Processor, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if proc, ok := c.processors[modelPath]; ok {
		return proc, nil
	}

	proc, err := sentencepiece.NewProcessorFromPath(modelPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load sentencepiece model from %q: %w", modelPath, err)
	}
	c.processors[modelPath] = proc
	return proc, nil
}
