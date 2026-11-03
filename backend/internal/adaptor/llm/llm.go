// Package llm 是 OpenAI 兼容的流式对话适配器；只负责协议、流解析与工具参数聚合。
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"qingfly/edu-mall/internal/domain"
)

type Client struct {
	http *http.Client
}

func New(client *http.Client) *Client {
	if client == nil {
		client = &http.Client{}
	}
	return &Client{http: client}
}

func (c *Client) Stream(ctx context.Context, req domain.LLMRequest, onDelta func(string) error) (domain.LLMTurn, error) {
	var turn domain.LLMTurn
	body := map[string]any{"model": req.Model, "messages": req.Messages, "stream": true, "max_tokens": req.MaxTokens}
	if len(req.Tools) > 0 {
		body["tools"] = req.Tools
	}
	payload, _ := json.Marshal(body)
	httpReq, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(req.BaseURL, "/")+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return turn, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+req.APIKey)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return turn, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return turn, errors.New("model request failed")
	}
	calls := map[int]*domain.ToolCall{}
	var content strings.Builder
	finished := false
	scan := bufio.NewScanner(io.LimitReader(resp.Body, 4<<20))
	scan.Buffer(make([]byte, 4096), 1<<20)
	for scan.Scan() {
		line := scan.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			finished = true
			break
		}
		var event struct {
			Choices []struct {
				Delta struct {
					Content   string            `json:"content"`
					ToolCalls []domain.ToolCall `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err = json.Unmarshal([]byte(data), &event); err != nil {
			resp.Body.Close()
			return turn, err
		}
		for _, choice := range event.Choices {
			if choice.FinishReason != nil {
				finished = true
				turn.FinishReason = *choice.FinishReason
			}
			if text := choice.Delta.Content; text != "" {
				content.WriteString(text)
				if content.Len() > 100000 {
					resp.Body.Close()
					return turn, errors.New("output limit")
				}
				if err = onDelta(text); err != nil {
					resp.Body.Close()
					return turn, err
				}
			}
			for _, d := range choice.Delta.ToolCalls {
				if d.Index < 0 || d.Index > 7 {
					resp.Body.Close()
					return turn, errors.New("tool limit")
				}
				call := calls[d.Index]
				if call == nil {
					call = &domain.ToolCall{Type: "function"}
					calls[d.Index] = call
				}
				if d.ID != "" {
					call.ID = d.ID
				}
				call.Function.Name += d.Function.Name
				call.Function.Arguments += d.Function.Arguments
				if len(call.Function.Arguments) > 8192 {
					resp.Body.Close()
					return turn, errors.New("tool arguments too large")
				}
			}
		}
	}
	scanErr := scan.Err()
	resp.Body.Close()
	if scanErr != nil {
		return turn, scanErr
	}
	if !finished {
		return turn, errors.New("incomplete model stream")
	}
	if turn.FinishReason != "stop" && turn.FinishReason != "tool_calls" {
		return turn, errors.New("model output incomplete or filtered")
	}
	turn.Content = content.String()
	if len(calls) == 0 {
		if turn.Content == "" {
			return turn, errors.New("empty model answer")
		}
		return turn, nil
	}
	for i := 0; i < 8; i++ {
		if call := calls[i]; call != nil {
			if call.ID == "" {
				return turn, errors.New("missing tool call id")
			}
			turn.ToolCalls = append(turn.ToolCalls, *call)
		}
	}
	return turn, nil
}
