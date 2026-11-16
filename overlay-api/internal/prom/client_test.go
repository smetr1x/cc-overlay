package prom

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func serve(t *testing.T, status int, body string, gotQuery *string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/query" {
			t.Errorf("запрос %s %s", r.Method, r.URL.Path)
		}
		if gotQuery != nil {
			*gotQuery = r.FormValue("query")
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL + "/")
}

func TestQueryVector(t *testing.T) {
	var got string
	c := serve(t, 200, `{"status":"success","data":{"resultType":"vector","result":[
		{"metric":{"type":"input"},"value":[1700000000.5,"150"]},
		{"metric":{"type":"ratio"},"value":[1700000000.5,"NaN"]}]}}`, &got)

	samples, err := c.Query(context.Background(), `sum by (type) (T{a="b"})`)
	if err != nil {
		t.Fatal(err)
	}
	if got != `sum by (type) (T{a="b"})` {
		t.Errorf("запрос дошёл искажённым: %q", got)
	}
	if len(samples) != 2 || samples[0].Labels["type"] != "input" || samples[0].Value != 150 {
		t.Errorf("результат: %+v", samples)
	}
	if !math.IsNaN(samples[1].Value) {
		t.Errorf("NaN должен разбираться как NaN: %v", samples[1].Value)
	}
}

func TestQueryEmptyIsNotError(t *testing.T) {
	c := serve(t, 200, `{"status":"success","data":{"resultType":"vector","result":[]}}`, nil)
	samples, err := c.Query(context.Background(), "up")
	if err != nil || len(samples) != 0 {
		t.Errorf("пустой результат: %v, %v", samples, err)
	}
}

func TestQueryErrors(t *testing.T) {
	for name, c := range map[string]*Client{
		"ошибка запроса": serve(t, 400, `{"status":"error","errorType":"bad_data","error":"parse error"}`, nil),
		"не JSON":        serve(t, 502, `<html>bad gateway</html>`, nil),
		"не vector":      serve(t, 200, `{"status":"success","data":{"resultType":"scalar","result":[]}}`, nil),
		"нет сервера":    New("http://127.0.0.1:1"),
	} {
		if _, err := c.Query(context.Background(), "up"); err == nil {
			t.Errorf("%s: ожидалась ошибка", name)
		}
	}
}
