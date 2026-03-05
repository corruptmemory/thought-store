package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type OllamaClient struct {
	baseURL string
	model   string
	http    *http.Client
}

func NewOllamaClient(baseURL, model string) *OllamaClient {
	return &OllamaClient{
		baseURL: baseURL,
		model:   model,
		http:    &http.Client{},
	}
}

type embedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type embedResponse struct {
	Embedding []float32 `json:"embedding"`
}

func (c *OllamaClient) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(embedRequest{Model: c.model, Prompt: text})
	if err != nil {
		return nil, fmt.Errorf("marshal embed request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}

	var result embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return result.Embedding, nil
}

type embedBatchRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedBatchResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// EmbedBatch embeds multiple texts in a single call using Ollama's /api/embed endpoint.
func (c *OllamaClient) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(embedBatchRequest{Model: c.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("marshal batch embed request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama batch request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}

	var result embedBatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode batch response: %w", err)
	}

	if len(result.Embeddings) != len(texts) {
		return nil, fmt.Errorf("expected %d embeddings, got %d", len(texts), len(result.Embeddings))
	}

	return result.Embeddings, nil
}

func (c *OllamaClient) Version(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/version", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var v struct{ Version string }
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", err
	}
	return v.Version, nil
}

// ModelInfo holds metadata about an Ollama model.
type ModelInfo struct {
	Family        string
	ParameterSize string
	Quantization  string
	EmbeddingDim  int
}

func (c *OllamaClient) ShowModel(ctx context.Context) (*ModelInfo, error) {
	body, _ := json.Marshal(struct {
		Name string `json:"name"`
	}{Name: c.model})

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/show", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var raw struct {
		Details struct {
			Family        string `json:"family"`
			ParameterSize string `json:"parameter_size"`
			Quantization  string `json:"quantization_level"`
		} `json:"details"`
		ModelInfo map[string]any `json:"model_info"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	info := &ModelInfo{
		Family:        raw.Details.Family,
		ParameterSize: raw.Details.ParameterSize,
		Quantization:  raw.Details.Quantization,
	}

	// Extract embedding dimension from model_info
	if dim, ok := raw.ModelInfo["bert.embedding_length"]; ok {
		if v, ok := dim.(float64); ok {
			info.EmbeddingDim = int(v)
		}
	}

	return info, nil
}

// Diagnostics gathers best-effort diagnostic info about the Ollama service.
func (c *OllamaClient) Diagnostics(ctx context.Context) *OllamaDiag {
	diag := &OllamaDiag{
		URL:    c.baseURL,
		Model:  c.model,
		Status: "ok",
	}

	ver, err := c.Version(ctx)
	if err != nil {
		diag.Status = "error"
		diag.Errors = append(diag.Errors, fmt.Sprintf("version: %v", err))
	} else {
		diag.Version = ver
	}

	info, err := c.ShowModel(ctx)
	if err != nil {
		diag.Errors = append(diag.Errors, fmt.Sprintf("show model: %v", err))
	} else {
		diag.Family = info.Family
		diag.ParameterSize = info.ParameterSize
		diag.Quantization = info.Quantization
		diag.EmbeddingDim = info.EmbeddingDim
	}

	return diag
}
