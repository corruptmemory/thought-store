package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"time"
)

var _ ThoughtStore = (*RemoteBrain)(nil)

// RemoteBrain implements ThoughtStore by talking to a thought-store HTTP server.
// It tries URLs in order, using the first one that responds.
type RemoteBrain struct {
	urls      []string
	client    *http.Client
	startedAt time.Time
}

func NewRemoteBrain(urls []string, timeout time.Duration) *RemoteBrain {
	return &RemoteBrain{
		urls:      urls,
		client:    &http.Client{Timeout: timeout},
		startedAt: time.Now(),
	}
}

func (b *RemoteBrain) Capture(ctx context.Context, content string, source string) (*Thought, error) {
	body, _ := json.Marshal(captureRequest{Content: content})
	resp, err := b.doRequest(ctx, "POST", "/api/thoughts", bytes.NewReader(body), http.Header{
		"Content-Type": {"application/json"},
		"X-Source":     {source},
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("capture failed (%d): %s", resp.StatusCode, msg)
	}

	var t Thought
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("decode thought: %w", err)
	}
	return &t, nil
}

func (b *RemoteBrain) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	path := fmt.Sprintf("/api/search?q=%s&limit=%d", url.QueryEscape(query), limit)
	resp, err := b.doRequest(ctx, "GET", path, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var results []SearchResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("decode search results: %w", err)
	}
	return results, nil
}

func (b *RemoteBrain) List(ctx context.Context, limit, offset int) ([]Thought, error) {
	path := fmt.Sprintf("/api/thoughts?limit=%d&offset=%d", limit, offset)
	resp, err := b.doRequest(ctx, "GET", path, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var thoughts []Thought
	if err := json.NewDecoder(resp.Body).Decode(&thoughts); err != nil {
		return nil, fmt.Errorf("decode thoughts: %w", err)
	}
	return thoughts, nil
}

func (b *RemoteBrain) Stats(ctx context.Context) (*StoreStats, error) {
	resp, err := b.doRequest(ctx, "GET", "/api/stats", nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var stats StoreStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, fmt.Errorf("decode stats: %w", err)
	}
	return &stats, nil
}

func (b *RemoteBrain) Diagnostics(ctx context.Context) (*DiagnosticsReport, error) {
	report := &DiagnosticsReport{
		Mode:      BrainModeRemote,
		StartedAt: b.startedAt,
		Uptime:    time.Since(b.startedAt).Truncate(time.Second).String(),
		GoVersion: runtime.Version(),
	}

	remoteDiag := &RemoteDiag{
		URLs: b.urls,
	}

	// Probe: find which URL responds and measure latency
	start := time.Now()
	resp, err := b.doRequest(ctx, "GET", "/api/diagnostics", nil, nil)
	latency := time.Since(start)

	if err != nil {
		remoteDiag.ActiveURL = "none"
		remoteDiag.NetworkType = "unreachable"
		remoteDiag.Latency = "n/a"
		report.Remote = remoteDiag
		return report, nil
	}
	defer resp.Body.Close()

	remoteDiag.Latency = latency.Truncate(time.Millisecond).String()

	// Determine which URL responded by probing each individually
	for _, u := range b.urls {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		req, reqErr := http.NewRequestWithContext(probeCtx, "GET", u+"/api/stats", nil)
		if reqErr != nil {
			cancel()
			continue
		}
		probeResp, probeErr := b.client.Do(req)
		cancel()
		if probeErr == nil {
			probeResp.Body.Close()
			remoteDiag.ActiveURL = u
			remoteDiag.NetworkType = NetworkType(u)
			break
		}
	}

	// Decode the server's diagnostics report
	var serverDiag DiagnosticsReport
	if err := json.NewDecoder(resp.Body).Decode(&serverDiag); err == nil {
		remoteDiag.ServerDiag = &serverDiag
	}

	report.Remote = remoteDiag
	return report, nil
}

// doRequest tries each URL in the fallback chain until one responds.
func (b *RemoteBrain) doRequest(ctx context.Context, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	var lastErr error
	for _, base := range b.urls {
		// Reset body reader for retry if it supports seeking
		if seeker, ok := body.(io.Seeker); ok {
			seeker.Seek(0, io.SeekStart)
		}

		req, err := http.NewRequestWithContext(ctx, method, base+path, body)
		if err != nil {
			lastErr = err
			continue
		}
		for k, v := range headers {
			req.Header[k] = v
		}

		resp, err := b.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("all URLs unreachable: %w", lastErr)
}
