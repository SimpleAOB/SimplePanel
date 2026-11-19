package docker

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDocker serves a tiny Docker API on a unix socket. While hang is set,
// every request except /_ping (when pingOK) blocks until the test ends.
func fakeDocker(t *testing.T) (*Client, *atomic.Bool, *atomic.Bool) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var hang, pingOK atomic.Bool
	pingOK.Store(true)
	done := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_ping" && pingOK.Load() {
			w.Write([]byte("OK"))
			return
		}
		if hang.Load() {
			select {
			case <-done:
			case <-r.Context().Done():
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	})}
	go srv.Serve(l)
	t.Cleanup(func() { close(done); srv.Close() })
	return New(sock), &hang, &pingOK
}

func shorten(t *testing.T) {
	old := [3]time.Duration{CallTimeout, probeStart, maxBackoff}
	CallTimeout, probeStart, maxBackoff = 200*time.Millisecond, 50*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { CallTimeout, probeStart, maxBackoff = old[0], old[1], old[2] })
}

func TestHungDockerFailsFastAndRecovers(t *testing.T) {
	shorten(t)
	c, hang, pingOK := fakeDocker(t)
	ctx := context.Background()
	if _, err := c.TasksOf(ctx, []string{"x"}); err != nil {
		t.Fatalf("healthy call: %v", err)
	}
	hang.Store(true)
	pingOK.Store(false)
	start := time.Now()
	_, err := c.TasksOf(ctx, []string{"x"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("call took %s, want about CallTimeout", d)
	}
	if c.Health().OK {
		t.Fatal("health should be down")
	}
	// While down, calls fail at once instead of piling up.
	start = time.Now()
	for i := 0; i < 50; i++ {
		if _, err := c.TasksOf(ctx, []string{"x"}); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("50 calls took %s while down", d)
	}
	// Docker comes back: the probe notices and calls work again.
	hang.Store(false)
	pingOK.Store(true)
	deadline := time.Now().Add(3 * time.Second)
	for !c.Health().OK {
		if time.Now().After(deadline) {
			t.Fatal("never recovered")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := c.TasksOf(ctx, []string{"x"}); err != nil {
		t.Fatalf("after recovery: %v", err)
	}
}

func TestLongCallsAreNotCutOff(t *testing.T) {
	shorten(t)
	c, hang, _ := fakeDocker(t)
	hang.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err := c.doJSON(Long(ctx), http.MethodPost, "/containers/x/wait", nil, nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the caller's deadline, got %v", err)
	}
	if !c.Health().OK {
		t.Fatal("a long call hitting its caller's deadline must not mark Docker down")
	}
}

func TestCallerCancelDoesNotMarkDown(t *testing.T) {
	shorten(t)
	c, hang, _ := fakeDocker(t)
	hang.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.TasksOf(ctx, []string{"x"}); err == nil {
		t.Fatal("want an error")
	}
	if !c.Health().OK {
		t.Fatal("the caller giving up is not Docker being down")
	}
}
