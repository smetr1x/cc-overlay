// Package prom — минимальный клиент HTTP API Prometheus: только мгновенные запросы.
package prom

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Sample — одна серия из результата мгновенного запроса.
type Sample struct {
	Labels map[string]string
	Value  float64
}

// Client выполняет запросы к одному серверу Prometheus.
type Client struct {
	baseURL string
	http    *http.Client
}

// New создаёт клиент. Таймауты задаёт вызывающий через контекст.
func New(baseURL string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{}}
}

type response struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// Query выполняет мгновенный запрос на текущий момент сервера.
// Пустой результат — это не ошибка: так Prometheus сообщает об отсутствии данных.
func (c *Client) Query(ctx context.Context, expr string) ([]Sample, error) {
	// POST с телом формы: запросы длинные и не должны упираться в предел длины URL.
	body := url.Values{"query": {expr}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/query", strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var r response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("prometheus: HTTP %d, ответ не разобран: %w", resp.StatusCode, err)
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("prometheus: %s", r.Error)
	}
	if r.Data.ResultType != "vector" {
		return nil, fmt.Errorf("prometheus: тип результата %q, ожидался vector", r.Data.ResultType)
	}

	out := make([]Sample, 0, len(r.Data.Result))
	for _, item := range r.Data.Result {
		s, ok := item.Value[1].(string)
		if !ok {
			return nil, fmt.Errorf("prometheus: значение %v не строка", item.Value[1])
		}
		v, err := strconv.ParseFloat(s, 64) // понимает и NaN, и +Inf
		if err != nil {
			return nil, fmt.Errorf("prometheus: значение %q: %w", s, err)
		}
		out = append(out, Sample{Labels: item.Metric, Value: v})
	}
	return out, nil
}
