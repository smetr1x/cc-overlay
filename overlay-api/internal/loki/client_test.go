package loki

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func serve(t *testing.T, check func(r *http.Request), status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			check(r)
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL + "/")
}

func TestQuery(t *testing.T) {
	at := time.Unix(1700000000, 0)
	c := serve(t, func(r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query" || r.URL.Query().Get("time") != "1700000000000000000" {
			t.Errorf("запрос: %s", r.URL)
		}
	}, 200, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"3"]}]}}`)

	s, err := c.Query(context.Background(), `sum(count_over_time({a="b"}[5m]))`, at)
	if err != nil || len(s) != 1 || s[0].Value != 3 {
		t.Errorf("результат: %+v, %v", s, err)
	}
}

func TestLastPicksNewestAcrossStreams(t *testing.T) {
	c := serve(t, func(r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/loki/api/v1/query_range" || q.Get("limit") != "1" || q.Get("direction") != "backward" {
			t.Errorf("запрос: %s", r.URL)
		}
	}, 200, `{"status":"success","data":{"resultType":"streams","result":[
		{"stream":{"model":"old","duration_ms":"100"},"values":[["1700000000000000000","claude_code.api_request"]]},
		{"stream":{"model":"new","duration_ms":"200"},"values":[["1700000005000000000","claude_code.api_request"]]}]}}`)

	e, err := c.Last(context.Background(), `{a="b"}`, time.Unix(0, 0), time.Unix(1800000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if e == nil || e.Labels["model"] != "new" || e.Time.Unix() != 1700000005 {
		t.Errorf("запись: %+v", e)
	}
}

func TestLastEmpty(t *testing.T) {
	c := serve(t, nil, 200, `{"status":"success","data":{"resultType":"streams","result":[]}}`)
	if e, err := c.Last(context.Background(), `{a="b"}`, time.Unix(0, 0), time.Unix(1, 0)); e != nil || err != nil {
		t.Errorf("пусто: %+v, %v", e, err)
	}
}

func TestErrors(t *testing.T) {
	for name, c := range map[string]*Client{
		"ошибка запроса": serve(t, nil, 400, "parse error at line 1"),
		"не JSON":        serve(t, nil, 200, "<html>"),
		"нет сервера":    New("http://127.0.0.1:1"),
	} {
		if _, err := c.Query(context.Background(), "x", time.Now()); err == nil {
			t.Errorf("%s: ожидалась ошибка", name)
		}
	}
	wrongType := serve(t, nil, 200, `{"status":"success","data":{"resultType":"streams","result":[]}}`)
	if _, err := wrongType.Query(context.Background(), "x", time.Now()); err == nil {
		t.Error("streams вместо vector: ожидалась ошибка")
	}
}
