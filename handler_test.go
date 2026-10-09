package asynqmon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hibiken/asynq"
)

// newTestHandler builds a handler for two instances. Redis clients connect
// lazily, so the addresses don't need to be reachable for routing tests.
func newTestHandler(t *testing.T, opts Options) *HTTPHandler {
	t.Helper()
	opts.RedisInstances = []RedisInstance{
		{ID: "prod", Name: "Production", RedisConnOpt: asynq.RedisClientOpt{Addr: "127.0.0.1:1"}},
		{ID: "staging", RedisConnOpt: asynq.RedisClientOpt{Addr: "127.0.0.1:1"}},
	}
	h := New(opts)
	t.Cleanup(func() { h.Close() })
	return h
}

func serve(h http.Handler, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

func TestListInstances(t *testing.T) {
	h := newTestHandler(t, Options{})
	w := serve(h, "GET", "/api/instances")
	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", w.Code)
	}
	var got []instanceInfo
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []instanceInfo{{ID: "prod", Name: "Production"}, {ID: "staging", Name: "staging"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestUnknownInstanceReturnsNotFound(t *testing.T) {
	h := newTestHandler(t, Options{})
	w := serve(h, "GET", "/api/instances/nope/queues")
	if w.Code != http.StatusNotFound {
		t.Errorf("got status %d, want 404", w.Code)
	}
}

func TestReadOnlyAppliesToEveryInstance(t *testing.T) {
	h := newTestHandler(t, Options{ReadOnly: true})
	for _, id := range []string{"prod", "staging"} {
		w := serve(h, "DELETE", "/api/instances/"+id+"/queues/default")
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("instance %q: got status %d, want 405", id, w.Code)
		}
	}
}

func TestIndexInjectsInstances(t *testing.T) {
	h := newTestHandler(t, Options{})
	w := serve(h, "GET", "/")
	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Production") {
		t.Errorf("index.html does not contain the injected instance list")
	}
}

func TestMetricsQueryIsScopedToInstance(t *testing.T) {
	var (
		mu      sync.Mutex
		queries []string
	)
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		queries = append(queries, r.URL.Query().Get("query"))
		w.Write([]byte(`{}`))
	}))
	defer prom.Close()

	h := newTestHandler(t, Options{PrometheusAddress: prom.URL, PrometheusInstanceLabel: "redis_instance"})
	w := serve(h, "GET", "/api/instances/staging/metrics?queues=critical")
	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200: %s", w.Code, w.Body.String())
	}
	if len(queries) == 0 {
		t.Fatal("no queries were sent to prometheus")
	}
	for _, q := range queries {
		if !strings.Contains(q, `redis_instance="staging"`) || !strings.Contains(q, `queue=~"critical"`) {
			t.Errorf("query %q is not scoped to the instance and queue", q)
		}
	}
}

func TestApplyQueueFilter(t *testing.T) {
	tests := []struct {
		qnames   []string
		instance string
		want     string
	}{
		{nil, "", `m{}`},
		{[]string{"a", "b"}, "", `m{queue=~"a|b"}`},
		{nil, `redis_instance="x"`, `m{redis_instance="x"}`},
		{[]string{"a"}, `redis_instance="x"`, `m{queue=~"a",redis_instance="x"}`},
	}
	for _, tc := range tests {
		if got := applyQueueFilter("m{QUEUE_FILTER}", tc.qnames, tc.instance); got != tc.want {
			t.Errorf("applyQueueFilter(%v, %q) = %q, want %q", tc.qnames, tc.instance, got, tc.want)
		}
	}
}

func TestNewRejectsInvalidInstanceID(t *testing.T) {
	for _, id := range []string{"", "a/b", "a b", "a?b"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("New did not panic for instance id %q", id)
				}
			}()
			New(Options{RedisInstances: []RedisInstance{{ID: id, RedisConnOpt: asynq.RedisClientOpt{Addr: "127.0.0.1:1"}}}})
		}()
	}
}
