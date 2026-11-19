package docker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// Docker can stop answering: under heavy memory pressure, or when Swarm's
// internal database is stuck. The panel must stay usable when that happens,
// so every call is bounded:
//
//   - short API calls time out after CallTimeout (long-running ones such as
//     builds, pulls, log streams and container waits are left alone);
//   - at most MaxInFlight short calls run at once, so a slow daemon can't
//     pile up hundreds of waiting requests;
//   - after a call times out or the socket can't be reached, Docker is
//     marked as not responding. Calls then fail straight away with
//     ErrUnavailable while a background probe pings Docker with backoff,
//     and everything resumes once it answers again.

// ErrUnavailable is returned while Docker is not responding.
var ErrUnavailable = errors.New("Docker is not responding")

// MaxInFlight is how many short calls may run at the same time.
const MaxInFlight = 8

// Variables so tests can shorten them.
var (
	// CallTimeout bounds a short Docker API call.
	CallTimeout  = 20 * time.Second
	probeTimeout = 5 * time.Second
	probeStart   = time.Second
	maxBackoff   = 15 * time.Second
)

// Health is Docker's state as the panel sees it.
type Health struct {
	OK      bool      `json:"ok"`
	Since   time.Time `json:"since,omitempty"` // when Docker stopped responding
	Error   string    `json:"error,omitempty"`
	Checked time.Time `json:"checked,omitempty"` // last probe
}

type healthState struct {
	mu      sync.Mutex
	down    bool
	since   time.Time
	lastErr string
	checked time.Time
	probing bool
	// onChange, if set, is called (in its own goroutine) when Docker stops
	// or starts responding again.
	onChange func(Health)
}

// Health reports whether Docker is responding.
func (c *Client) Health() Health {
	c.h.mu.Lock()
	defer c.h.mu.Unlock()
	if !c.h.down {
		return Health{OK: true, Checked: c.h.checked}
	}
	return Health{Since: c.h.since, Error: c.h.lastErr, Checked: c.h.checked}
}

// OnHealthChange registers a function called when Docker stops or starts
// responding.
func (c *Client) OnHealthChange(fn func(Health)) {
	c.h.mu.Lock()
	c.h.onChange = fn
	c.h.mu.Unlock()
}

// available returns ErrUnavailable while Docker is marked as down.
func (c *Client) available() error {
	c.h.mu.Lock()
	down := c.h.down
	c.h.mu.Unlock()
	if down {
		return ErrUnavailable
	}
	return nil
}

// markDown records that Docker didn't answer and starts the probe.
func (c *Client) markDown(err error) {
	c.h.mu.Lock()
	c.h.lastErr = err.Error()
	changed := !c.h.down
	if changed {
		c.h.down, c.h.since = true, time.Now()
	}
	start := !c.h.probing
	c.h.probing = true
	fn := c.h.onChange
	h := Health{Since: c.h.since, Error: c.h.lastErr}
	c.h.mu.Unlock()
	if changed && fn != nil {
		go fn(h)
	}
	if start {
		go c.probe()
	}
}

// probe pings Docker with growing pauses until it answers.
func (c *Client) probe() {
	wait := probeStart
	for {
		time.Sleep(wait)
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		err := c.ping(ctx)
		cancel()
		c.h.mu.Lock()
		c.h.checked = time.Now()
		if err == nil {
			c.h.down, c.h.probing, c.h.lastErr = false, false, ""
			fn := c.h.onChange
			c.h.mu.Unlock()
			if fn != nil {
				go fn(Health{OK: true, Checked: time.Now()})
			}
			return
		}
		c.h.lastErr = err.Error()
		c.h.mu.Unlock()
		wait = min(wait*2, maxBackoff)
	}
}

func (c *Client) ping(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/_ping", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ping: HTTP %d", resp.StatusCode)
	}
	return nil
}

type longKey struct{}

// Long marks ctx for a call that may legitimately take a long time (for
// example waiting for a container to exit), so it isn't cut off at
// CallTimeout. The caller's own deadline still applies.
func Long(ctx context.Context) context.Context { return context.WithValue(ctx, longKey{}, true) }

func isLong(ctx context.Context) bool { v, _ := ctx.Value(longKey{}).(bool); return v }

// shortCall bounds a short API call: it takes a slot (waiting at most
// CallTimeout for one) and applies the timeout. done must be called when
// the response has been read; it reports a timeout or an unreachable
// socket as Docker not responding.
func (c *Client) shortCall(ctx context.Context) (context.Context, func(error), error) {
	if err := c.available(); err != nil {
		return nil, nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	select {
	case c.slots <- struct{}{}:
	case <-cctx.Done():
		cancel()
		if ctx.Err() == nil { // our timeout, not the caller giving up
			err := fmt.Errorf("%w: %d requests already waiting", ErrUnavailable, MaxInFlight)
			c.markDown(err)
			return nil, nil, err
		}
		return nil, nil, ctx.Err()
	}
	return cctx, func(err error) {
		<-c.slots
		timedOut := cctx.Err() != nil && ctx.Err() == nil
		cancel()
		if timedOut {
			c.markDown(fmt.Errorf("a request took longer than %s", CallTimeout))
		} else if unreachable(err) {
			c.markDown(err)
		}
	}, nil
}

// unreachable reports an error dialling the socket (Docker stopped).
func unreachable(err error) bool {
	var op *net.OpError
	return err != nil && errors.As(err, &op) && op.Op == "dial"
}

// wrapUnavailable turns a context timeout from shortCall into ErrUnavailable
// so callers see one consistent error.
func (c *Client) wrapUnavailable(ctx context.Context, err error) error {
	if err == nil || ctx.Err() != nil {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w (no answer within %s)", ErrUnavailable, CallTimeout)
	}
	if unreachable(err) {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return err
}
