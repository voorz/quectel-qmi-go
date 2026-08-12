//go:build !linux

package qmi

import "context"

func openQRTRTransport(ctx context.Context, opts ClientOptions) (qmiTransport, error) {
	return nil, ErrQRTRUnsupported
}
