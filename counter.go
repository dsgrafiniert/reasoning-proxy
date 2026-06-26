package main

type Counter interface {
	Count(text string) int
}

type HeuristicCounter struct {
	charsPerToken float64
}

func NewHeuristicCounter(charsPerToken float64) *HeuristicCounter {
	if charsPerToken <= 0 {
		charsPerToken = 3.5
	}
	return &HeuristicCounter{charsPerToken: charsPerToken}
}

func (c *HeuristicCounter) Count(text string) int {
	if text == "" {
		return 0
	}
	return int(float64(len([]rune(text))) / c.charsPerToken)
}
