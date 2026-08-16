package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/logging"
)

// Errors returned by the client.
var (
	ErrEmptyCompletion = errors.New("model returned an empty completion")
	ErrDisabled        = errors.New("llm integration is disabled")
)

// Message is one chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Chat roles.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Usage reports token accounting when the server provides it.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Completion is one model answer.
type Completion struct {
	Content   string
	Usage     Usage
	LatencyMS int
}

// Client is a minimal typed client for OpenAI-compatible chat completions.
// No SDK is used: the surface needed here is one endpoint, and a hand-written
// client keeps the failure modes visible.
type Client struct {
	cfg  config.LLMConfig
	http *http.Client
	log  *slog.Logger
	sem  chan struct{}

	mu        sync.RWMutex
	lastOK    time.Time
	lastError string
}

// NewClient builds the LLM client with a concurrency semaphore sized by config.
func NewClient(cfg config.LLMConfig, logger *slog.Logger) *Client {
	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.Timeout},
		log:  logging.For(logger, logging.CategoryLLM),
		sem:  make(chan struct{}, maxConcurrent),
	}
}

// Enabled reports whether inference is configured to run at all.
func (c *Client) Enabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg.Enabled
}

// Model returns the configured model name.
func (c *Client) Model() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg.Model
}

// PromptVersion returns the configured prompt version.
func (c *Client) PromptVersion() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg.PromptVersion
}

// Config exposes the client configuration to the orchestration layer.
func (c *Client) Config() config.LLMConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg
}

// SetConfig applies edited settings. The concurrency semaphore is rebuilt only
// when its size actually changes, so in-flight requests are never disturbed.
func (c *Client) SetConfig(cfg config.LLMConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if cfg.MaxConcurrent < 1 {
		cfg.MaxConcurrent = 1
	}
	if cfg.MaxConcurrent != c.cfg.MaxConcurrent {
		c.sem = make(chan struct{}, cfg.MaxConcurrent)
	}
	if cfg.Timeout != c.cfg.Timeout && cfg.Timeout > 0 {
		c.http = &http.Client{Timeout: cfg.Timeout}
	}
	c.cfg = cfg
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Complete performs one chat completion. Concurrency is capped by the semaphore
// so a single local GPU is never asked to serve several requests at once.
func (c *Client) Complete(ctx context.Context, messages []Message) (*Completion, error) {
	c.mu.RLock()
	cfg := c.cfg
	sem := c.sem
	httpClient := c.http
	c.mu.RUnlock()

	if !cfg.Enabled {
		return nil, ErrDisabled
	}

	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	body, err := json.Marshal(chatRequest{
		Model:       cfg.Model,
		Messages:    messages,
		Temperature: cfg.Temperature,
		MaxTokens:   cfg.MaxTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("encode chat request: %w", err)
	}

	url := cfg.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	start := time.Now()
	resp, err := httpClient.Do(req)
	if err != nil {
		c.recordError(err)
		return nil, fmt.Errorf("llm request to %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		c.recordError(err)
		return nil, fmt.Errorf("read llm response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("llm returned http %d: %s", resp.StatusCode, truncate(string(raw), 400))
		c.recordError(err)
		return nil, err
	}

	var parsed chatResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		c.recordError(err)
		return nil, fmt.Errorf("decode llm response: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		err := fmt.Errorf("llm error: %s", parsed.Error.Message)
		c.recordError(err)
		return nil, err
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		c.recordError(ErrEmptyCompletion)
		return nil, ErrEmptyCompletion
	}

	c.recordSuccess()
	return &Completion{
		Content:   parsed.Choices[0].Message.Content,
		Usage:     parsed.Usage,
		LatencyMS: int(time.Since(start).Milliseconds()),
	}, nil
}

// Ping checks that the inference server answers. It uses the models endpoint,
// which every OpenAI-compatible server exposes and which costs no GPU time.
func (c *Client) Ping(ctx context.Context) error {
	c.mu.RLock()
	cfg := c.cfg
	c.mu.RUnlock()

	if !cfg.Enabled {
		return ErrDisabled
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.BaseURL+"/models", nil)
	if err != nil {
		return fmt.Errorf("build models request: %w", err)
	}
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("llm unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode >= 400 {
		return fmt.Errorf("llm health check returned http %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) recordSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastOK = time.Now().UTC()
	c.lastError = ""
}

func (c *Client) recordError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastError = err.Error()
}

// LastState reports the last successful call and the last error seen.
func (c *Client) LastState() (time.Time, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastOK, c.lastError
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
