package qmi

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

var errUnexpectedProxyOpen = errors.New("proxy transport should not have been opened")

func replaceQRTRTransportForTest(t *testing.T, hook func(context.Context, ClientOptions) (qmiTransport, error)) func() {
	t.Helper()

	old := openQRTRTransportHook
	openQRTRTransportHook = hook
	restored := false
	return func() {
		if restored {
			return
		}
		openQRTRTransportHook = old
		restored = true
	}
}

func TestNewClientWithOptionsUsesQRTRTransportWhenRequested(t *testing.T) {
	qrtrAttempts := 0
	restoreQRTR := replaceQRTRTransportForTest(t, func(context.Context, ClientOptions) (qmiTransport, error) {
		qrtrAttempts++
		clientConn, serverConn := net.Pipe()
		t.Cleanup(func() { serverConn.Close() })
		return clientConn, nil
	})
	defer restoreQRTR()

	proxyAttempts := 0
	restoreProxy := replaceProxyTransportForTest(t, func(context.Context, ClientOptions) (qmiTransport, error) {
		proxyAttempts++
		return nil, errUnexpectedProxyOpen
	})
	defer restoreProxy()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client, err := NewClientWithOptions(ctx, "", ClientOptions{
		UseQRTR:      true,
		SyncOnOpen:   false,
		ReadDeadline: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClientWithOptions() error = %v", err)
	}
	defer client.Close()

	if qrtrAttempts != 1 {
		t.Fatalf("qrtr transport attempts = %d, want 1", qrtrAttempts)
	}
	if proxyAttempts != 0 {
		t.Fatalf("proxy transport attempts = %d, want 0", proxyAttempts)
	}
}

func TestNewClientWithOptionsQRTRTakesPrecedenceOverProxy(t *testing.T) {
	qrtrAttempts := 0
	restoreQRTR := replaceQRTRTransportForTest(t, func(context.Context, ClientOptions) (qmiTransport, error) {
		qrtrAttempts++
		clientConn, serverConn := net.Pipe()
		t.Cleanup(func() { serverConn.Close() })
		return clientConn, nil
	})
	defer restoreQRTR()

	proxyOpenAttempts := 0
	restoreProxy := replaceProxyTransportForTest(t, func(context.Context, ClientOptions) (qmiTransport, error) {
		proxyOpenAttempts++
		return nil, errUnexpectedProxyOpen
	})
	defer restoreProxy()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Both UseQRTR and UseProxy set: this is a caller misconfiguration, but
	// UseQRTR must win and the proxy path (including its CTLInternalProxyOpen
	// handshake, which qrtrTransport does not support) must never run.
	client, err := NewClientWithOptions(ctx, "/dev/cdc-wdm-should-be-ignored", ClientOptions{
		UseQRTR:      true,
		UseProxy:     true,
		SyncOnOpen:   false,
		ReadDeadline: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClientWithOptions() error = %v", err)
	}
	defer client.Close()

	if qrtrAttempts != 1 {
		t.Fatalf("qrtr transport attempts = %d, want 1", qrtrAttempts)
	}
	if proxyOpenAttempts != 0 {
		t.Fatalf("proxy transport attempts = %d, want 0 (UseQRTR must take precedence)", proxyOpenAttempts)
	}
}
