package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestCancelledSwedaviaRequestDoesNotPoisonSharedBoardCache(t *testing.T) {
	started := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(started)
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, `{"flights":[]}`)
	}))
	defer server.Close()
	client := NewSwedavia(server.Client(), "fixture-key")
	client.BaseURL = server.URL
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := client.Board(ctx, "ARN", "arrivals", ""); result <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request not started")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not propagated: %v", err)
	}
	if _, err := client.Board(context.Background(), "ARN", "arrivals", ""); err != nil {
		t.Fatalf("cancellation persisted into next selection: %v", err)
	}
	if requests.Load() != 3 {
		t.Fatalf("expected cancelled request plus two fresh UTC-day requests, got %d", requests.Load())
	}
}
