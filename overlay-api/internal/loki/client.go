// Package loki — минимальный клиент HTTP API Loki: метрические запросы и последняя запись потока.
package loki

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cc-overlay/overlay-api/internal/prom"
)

// Entry — одна запись журнала. Labels содержит и метки потока, и structured metadata:
// Loki по умолчанию отдаёт их вместе. Точки в именах атрибутов OTLP заменены на «_».
type Entry struct {
	Time   time.Time
	Labels map[string]string
}

// Client выполняет запросы к одному серверу Loki.
type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{}}
}

type response struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string            `json:"resultType"`
		Result     []json.RawMessage `json:"result"`
	} `json:"data"`
}

func (c *Client) get(ctx context.Context, path string, params url.Values) (*response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+"?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Ошибки Loki приходят текстом, а не JSON.
		var buf [512]byte
		n, _ := resp.Body.Read(buf[:])
		return nil, fmt.Errorf("loki: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(buf[:n])))
	}
	var r response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("loki: ответ не разобран: %w", err)
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("loki: статус %q", r.Status)
	}
	return &r, nil
}

// Query выполняет метрический запрос LogQL на момент at. Пустой результат — не ошибка.
func (c *Client) Query(ctx context.Context, expr string, at time.Time) ([]prom.Sample, error) {
	r, err := c.get(ctx, "/loki/api/v1/query", url.Values{
		"query": {expr},
		"time":  {strconv.FormatInt(at.UnixNano(), 10)},
	})
	if err != nil {
		return nil, err
	}
	if r.Data.ResultType != "vector" {
		return nil, fmt.Errorf("loki: тип результата %q, ожидался vector", r.Data.ResultType)
	}
	out := make([]prom.Sample, 0, len(r.Data.Result))
	for _, raw := range r.Data.Result {
		var item struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		s, ok := item.Value[1].(string)
		if !ok {
			return nil, fmt.Errorf("loki: значение %v не строка", item.Value[1])
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, err
		}
		out = append(out, prom.Sample{Labels: item.Metric, Value: v})
	}
	return out, nil
}

// Last возвращает самую позднюю запись, подходящую под запрос, в интервале [start, end],
// или nil, если записей нет.
func (c *Client) Last(ctx context.Context, expr string, start, end time.Time) (*Entry, error) {
	r, err := c.get(ctx, "/loki/api/v1/query_range", url.Values{
		"query":     {expr},
		"start":     {strconv.FormatInt(start.UnixNano(), 10)},
		"end":       {strconv.FormatInt(end.UnixNano(), 10)},
		"limit":     {"1"},
		"direction": {"backward"},
	})
	if err != nil {
		return nil, err
	}
	if r.Data.ResultType != "streams" {
		return nil, fmt.Errorf("loki: тип результата %q, ожидался streams", r.Data.ResultType)
	}
	// Каждая комбинация меток — отдельный поток, поэтому при limit=1 потоков может быть несколько:
	// выбирается запись с наибольшим временем.
	var best *Entry
	for _, raw := range r.Data.Result {
		var stream struct {
			Stream map[string]string `json:"stream"`
			Values [][2]string       `json:"values"`
		}
		if err := json.Unmarshal(raw, &stream); err != nil {
			return nil, err
		}
		for _, v := range stream.Values {
			ns, err := strconv.ParseInt(v[0], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("loki: время записи %q: %w", v[0], err)
			}
			t := time.Unix(0, ns)
			if best == nil || t.After(best.Time) {
				best = &Entry{Time: t, Labels: stream.Stream}
			}
		}
	}
	return best, nil
}
