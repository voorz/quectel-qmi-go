package qmi

import "sync"

// ============================================================================
// Service Indication Handler
//
// Some QMI services (e.g., PDC) use an async indication-driven pattern where
// the synchronous response only carries a result code + token, and the actual
// payload is delivered via a subsequent indication matched by token.
//
// This file provides the mechanism for per-service indication handlers to be
// registered on the Client. When a registered service's indication arrives,
// the handler is invoked in addition to the normal event dispatch.
//
// Uses a global registry to avoid modifying the already-large client.go file.
// ============================================================================

// ServiceIndicationHandler is a callback invoked when an indication for a
// registered service arrives. The handler receives the raw Packet.
type ServiceIndicationHandler func(pkt *Packet)

// globalIndicationHandlerRegistry maps service ID → handler.
// Keyed by *Client pointer + service to support multiple clients.
var globalIndicationHandlerRegistry sync.Map

// indicationHandlerKey is the composite key for the global registry.
type indicationHandlerKey struct {
	client  *Client
	service uint8
}

// RegisterServiceIndicationHandler registers a callback for indications of
// the given service. When an indication for this service arrives, the handler
// is called in addition to being dispatched through the normal event channel.
//
// Only one handler per (client, service) pair is supported. Registering twice
// replaces the previous handler.
func (c *Client) RegisterServiceIndicationHandler(service uint8, handler ServiceIndicationHandler) {
	globalIndicationHandlerRegistry.Store(indicationHandlerKey{client: c, service: service}, handler)
}

// UnregisterServiceIndicationHandler removes a previously registered handler.
func (c *Client) UnregisterServiceIndicationHandler(service uint8) {
	globalIndicationHandlerRegistry.Delete(indicationHandlerKey{client: c, service: service})
}

// dispatchToServiceHandler calls the registered service indication handler
// if one exists for the given service. Returns true if a handler was called.
func (c *Client) dispatchToServiceHandler(pkt *Packet) bool {
	val, ok := globalIndicationHandlerRegistry.Load(indicationHandlerKey{client: c, service: pkt.ServiceType})
	if !ok {
		return false
	}
	handler := val.(ServiceIndicationHandler)
	if handler == nil {
		return false
	}
	handler(pkt)
	return true
}
