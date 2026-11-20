package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Streaming helpers: exec and helper containers with stdin, used by backups
// and restores to move data in and out of containers without temp files.

// hijack sends a request that Docker upgrades to a raw bidirectional stream
// (exec start and container attach). The caller must close the connection.
func (c *Client) hijack(ctx context.Context, path string, q url.Values, body any) (net.Conn, *bufio.Reader, error) {
	if err := c.availableFor(ctx); err != nil {
		return nil, nil, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.socket)
	if err != nil {
		if ctx.Err() == nil {
			c.markDown(err)
		}
		return nil, nil, fmt.Errorf("docker: cannot reach daemon: %w", err)
	}
	var rd io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			conn.Close()
			return nil, nil, err
		}
		rd = bytes.NewReader(b)
	}
	u := "http://docker/v" + c.version + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, rd)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		defer conn.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(b, &e) != nil || e.Message == "" {
			e.Message = strings.TrimSpace(string(b))
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, nil, ErrNotFound
		}
		return nil, nil, &APIError{Status: resp.StatusCode, Message: e.Message}
	}
	// Stop the stream when the context ends.
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
	return &stopConn{Conn: conn, stop: stop}, br, nil
}

type stopConn struct {
	net.Conn
	stop func() bool
}

func (c *stopConn) Close() error {
	c.stop()
	return c.Conn.Close()
}

func (c *stopConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// pump writes stdin (if any) to conn, then half-closes it, and demuxes the
// output into stdout and stderr until the stream ends.
func pump(conn net.Conn, br *bufio.Reader, stdin io.Reader, stdout, stderr io.Writer) error {
	inErr := make(chan error, 1)
	if stdin != nil {
		go func() {
			_, err := io.Copy(conn, stdin)
			if cw, ok := conn.(interface{ CloseWrite() error }); ok {
				cw.CloseWrite()
			}
			inErr <- err
		}()
	} else {
		inErr <- nil
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	err := demuxSplit(br, stdout, stderr)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	if ierr := <-inErr; ierr != nil && err == nil {
		err = fmt.Errorf("sending input: %w", ierr)
	}
	return err
}

// ExecStream runs cmd in a running container, streaming stdin to it and its
// output to stdout and stderr, and returns the exit code.
func (c *Client) ExecStream(ctx context.Context, containerID string, cmd, env []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	var created struct{ Id string }
	err := c.doJSON(ctx, http.MethodPost, "/containers/"+url.PathEscape(containerID)+"/exec", nil, map[string]any{
		"AttachStdin": stdin != nil, "AttachStdout": true, "AttachStderr": true, "Cmd": cmd, "Env": env,
	}, &created)
	if err != nil {
		return -1, err
	}
	conn, br, err := c.hijack(ctx, "/exec/"+created.Id+"/start", nil, map[string]bool{"Detach": false, "Tty": false})
	if err != nil {
		return -1, err
	}
	err = pump(conn, br, stdin, stdout, stderr)
	conn.Close()
	if err != nil {
		return -1, err
	}
	var info struct{ ExitCode int }
	if err := c.doJSON(ctx, http.MethodGet, "/exec/"+created.Id+"/json", nil, nil, &info); err != nil {
		return -1, err
	}
	return info.ExitCode, nil
}

// RunOptions describes a short-lived helper container.
type RunOptions struct {
	Image  string
	Cmd    []string
	Env    []string
	Mounts []Mount
}

// RunContainer runs a helper container to completion with no network,
// streaming stdin into it and its output out, then removes it.
func (c *Client) RunContainer(ctx context.Context, opt RunOptions, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	body := map[string]any{
		"Image": opt.Image, "Cmd": opt.Cmd, "Env": opt.Env,
		"AttachStdin": stdin != nil, "OpenStdin": stdin != nil, "StdinOnce": stdin != nil,
		"AttachStdout": true, "AttachStderr": true, "Tty": false,
		"Labels": map[string]string{"simplepanel.managed": "true", "simplepanel.helper": "true"},
		"HostConfig": map[string]any{
			"Mounts": opt.Mounts, "NetworkMode": "none", "AutoRemove": false,
			"SecurityOpt": []string{"no-new-privileges"}, "CapDrop": []string{"NET_RAW", "MKNOD", "AUDIT_WRITE"},
		},
	}
	var created struct{ Id string }
	if err := c.doJSON(ctx, http.MethodPost, "/containers/create", nil, body, &created); err != nil {
		return -1, err
	}
	defer func() {
		rctx, cancel := context.WithTimeout(Cleanup(context.WithoutCancel(ctx)), 30*time.Second)
		defer cancel()
		_ = c.doJSON(rctx, http.MethodDelete, "/containers/"+created.Id, url.Values{"force": {"1"}, "v": {"0"}}, nil, nil)
	}()
	q := url.Values{"stream": {"1"}, "stdout": {"1"}, "stderr": {"1"}}
	if stdin != nil {
		q.Set("stdin", "1")
	}
	conn, br, err := c.hijack(ctx, "/containers/"+created.Id+"/attach", q, nil)
	if err != nil {
		return -1, err
	}
	defer conn.Close()
	if err := c.doJSON(ctx, http.MethodPost, "/containers/"+created.Id+"/start", nil, nil, nil); err != nil {
		return -1, err
	}
	if err := pump(conn, br, stdin, stdout, stderr); err != nil {
		return -1, err
	}
	var wait struct{ StatusCode int }
	if err := c.doJSON(Long(ctx), http.MethodPost, "/containers/"+created.Id+"/wait", nil, nil, &wait); err != nil {
		return -1, err
	}
	return wait.StatusCode, nil
}

// ImageExists reports whether ref is in the local image store.
func (c *Client) ImageExists(ctx context.Context, ref string) bool {
	err := c.doJSON(ctx, http.MethodGet, "/images/"+url.PathEscape(ref)+"/json", nil, nil, nil)
	return err == nil
}

// ---- Stats ----

// Container is the subset of /containers/json we use.
type Container struct {
	ID     string            `json:"Id"`
	Labels map[string]string `json:"Labels"`
	State  string            `json:"State"`
}

// ManagedContainers lists running containers of SimplePanel services.
func (c *Client) ManagedContainers(ctx context.Context) ([]Container, error) {
	f, _ := json.Marshal(map[string][]string{"label": {"simplepanel.app"}, "status": {"running"}})
	var out []Container
	err := c.doJSON(ctx, http.MethodGet, "/containers/json", url.Values{"filters": {string(f)}}, nil, &out)
	return out, err
}

// Stats is one cumulative resource reading of a container.
type Stats struct {
	CPUTotal  uint64 // container CPU time, ns
	SystemCPU uint64 // host CPU time across all CPUs, ns
	OnlineCPU int
	Mem       int64 // working set: usage minus inactive page cache
	MemLimit  int64
	NetRx     uint64
	NetTx     uint64
}

// ContainerStats takes a single reading without waiting for a second one.
func (c *Client) ContainerStats(ctx context.Context, id string) (*Stats, error) {
	var raw struct {
		CPUStats struct {
			CPUUsage struct {
				TotalUsage uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			SystemUsage uint64 `json:"system_cpu_usage"`
			OnlineCPUs  int    `json:"online_cpus"`
		} `json:"cpu_stats"`
		MemoryStats struct {
			Usage int64            `json:"usage"`
			Limit int64            `json:"limit"`
			Stats map[string]int64 `json:"stats"`
		} `json:"memory_stats"`
		Networks map[string]struct {
			RxBytes uint64 `json:"rx_bytes"`
			TxBytes uint64 `json:"tx_bytes"`
		} `json:"networks"`
	}
	q := url.Values{"stream": {"false"}, "one-shot": {"true"}}
	if err := c.doJSON(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/stats", q, nil, &raw); err != nil {
		return nil, err
	}
	st := &Stats{
		CPUTotal: raw.CPUStats.CPUUsage.TotalUsage, SystemCPU: raw.CPUStats.SystemUsage, OnlineCPU: raw.CPUStats.OnlineCPUs,
		Mem: raw.MemoryStats.Usage, MemLimit: raw.MemoryStats.Limit,
	}
	// Same working-set calculation as `docker stats`.
	if v, ok := raw.MemoryStats.Stats["inactive_file"]; ok && v < st.Mem {
		st.Mem -= v
	} else if v, ok := raw.MemoryStats.Stats["total_inactive_file"]; ok && v < st.Mem {
		st.Mem -= v
	}
	for _, n := range raw.Networks {
		st.NetRx += n.RxBytes
		st.NetTx += n.TxBytes
	}
	return st, nil
}

// AppIDLabel returns the SimplePanel app id of a container, or 0.
func AppIDLabel(c Container) int64 {
	id, _ := strconv.ParseInt(c.Labels["simplepanel.app"], 10, 64)
	return id
}
