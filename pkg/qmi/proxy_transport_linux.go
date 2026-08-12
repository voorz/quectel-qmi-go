//go:build linux

package qmi

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type proxyStartAttempt struct {
	done chan struct{}
	err  error
}

var (
	dialProxyHook         = dialProxy
	startProxyProcessHook = startProxyProcess
	proxyRetryDelay       = 100 * time.Millisecond
	proxyStartMu          sync.Mutex
	proxyStartAttempts    = make(map[string]*proxyStartAttempt)
)

func openProxyTransport(ctx context.Context, opts ClientOptions) (qmiTransport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	proxyPath := opts.ProxyPath
	if proxyPath == "" {
		proxyPath = defaultProxyPath
	}
	proxyExecutable := opts.ProxyExecutable
	if proxyExecutable == "" {
		proxyExecutable = defaultProxyExecutable
	}

	conn, firstErr := dialProxyHook(ctx, proxyPath)
	if firstErr == nil {
		return conn, nil
	}

	if proxyExecutable == "" {
		return nil, fmt.Errorf("connect qmi-proxy %q: %w", proxyPath, firstErr)
	}
	if _, err := os.Stat(proxyExecutable); err != nil {
		return nil, fmt.Errorf("connect qmi-proxy %q failed: %w; proxy executable %s is unavailable: %v", proxyPath, firstErr, proxyExecutable, err)
	}

	// One caller owns the startup attempt for a socket. Other callers wait for
	// that attempt to either establish a connection or fail, instead of each
	// forking a proxy while the first process is still binding its socket.
	key := proxySocketAddress(proxyPath)
	proxyStartMu.Lock()
	if attempt, ok := proxyStartAttempts[key]; ok {
		proxyStartMu.Unlock()
		return waitForProxyStart(ctx, proxyPath, attempt)
	}
	attempt := &proxyStartAttempt{done: make(chan struct{})}
	proxyStartAttempts[key] = attempt
	proxyStartMu.Unlock()

	conn, err := startProxyAndWait(ctx, proxyPath, proxyExecutable, firstErr)
	proxyStartMu.Lock()
	attempt.err = err
	delete(proxyStartAttempts, key)
	close(attempt.done)
	proxyStartMu.Unlock()
	return conn, err
}

func waitForProxyStart(ctx context.Context, proxyPath string, attempt *proxyStartAttempt) (qmiTransport, error) {
	select {
	case <-attempt.done:
		if attempt.err != nil {
			return nil, attempt.err
		}
		conn, err := dialProxyHook(ctx, proxyPath)
		if err != nil {
			return nil, fmt.Errorf("connect qmi-proxy %q after startup: %w", proxyPath, err)
		}
		return conn, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for qmi-proxy %q startup: %w", proxyPath, ctx.Err())
	}
}

func startProxyAndWait(ctx context.Context, proxyPath, proxyExecutable string, firstErr error) (qmiTransport, error) {
	// The first dial may have raced with an externally started proxy. Check once
	// more after becoming the owner before creating a new process.
	if conn, err := dialProxyHook(ctx, proxyPath); err == nil {
		return conn, nil
	}
	if err := startProxyProcessHook(proxyExecutable); err != nil {
		return nil, fmt.Errorf("connect qmi-proxy %q failed and start %s failed: %w", proxyPath, proxyExecutable, err)
	}

	var lastErr error = firstErr
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("connect qmi-proxy %q after starting %s: last error: %v: %w", proxyPath, proxyExecutable, lastErr, err)
		}
		timer := time.NewTimer(proxyRetryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, fmt.Errorf("connect qmi-proxy %q after starting %s: last error: %v: %w", proxyPath, proxyExecutable, lastErr, ctx.Err())
		case <-timer.C:
		}
		conn, err := dialProxyHook(ctx, proxyPath)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
}

func dialProxy(ctx context.Context, proxyPath string) (qmiTransport, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, "unix", proxySocketAddress(proxyPath))
}

func proxySocketAddress(proxyPath string) string {
	if proxyPath == "" {
		proxyPath = defaultProxyPath
	}
	if strings.HasPrefix(proxyPath, "\x00") {
		return proxyPath
	}
	if strings.HasPrefix(proxyPath, "@") {
		return "\x00" + strings.TrimPrefix(proxyPath, "@")
	}
	return "\x00" + proxyPath
}

func startProxyProcess(proxyExecutable string) error {
	cmd := exec.Command(proxyExecutable)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	// qmi-proxy is intentionally long-lived, so wait asynchronously. Release
	// only drops the Go process handle and leaves an exited child as a zombie.
	go func() { _ = cmd.Wait() }()
	return nil
}
