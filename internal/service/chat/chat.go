// Package chat is a compatibility shim over
// github.com/richardwooding/parley/service/chat, where the chat service was
// extracted (kibitz was its first consumer).
package chat

import pchat "github.com/richardwooding/parley/service/chat"

// ID is the chat service identifier on the wire.
const ID = pchat.ID

type (
	// Service is the chat service; embed-register it on the mux.
	Service = pchat.Service
	// Message is the mux event emitted for every chat line.
	Message = pchat.Message
)

// New constructs an empty chat service.
func New() *Service { return pchat.New() }
