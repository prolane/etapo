package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// SSEBroker manages connected Server-Sent Events clients.
type SSEBroker struct {
	clientsMu sync.RWMutex
	clients   map[chan string]bool
	closed    chan struct{}
}

// NewSSEBroker creates an SSE broadcaster.
func NewSSEBroker() *SSEBroker {
	return &SSEBroker{
		clients: make(map[chan string]bool),
		closed:  make(chan struct{}),
	}
}

// Close ends all open SSE streams so the HTTP server can shut down without waiting for them.
func (b *SSEBroker) Close() {
	close(b.closed)
}

// Broadcast pushes an event string and data payload to all connected SSE clients.
func (b *SSEBroker) Broadcast(event string, data interface{}) {
	payloadBytes, err := json.Marshal(data)
	if err != nil {
		return
	}

	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", event, string(payloadBytes))

	b.clientsMu.RLock()
	defer b.clientsMu.RUnlock()

	for clientChan := range b.clients {
		select {
		case clientChan <- msg:
		default:
			// Non-blocking drop if client is slow
		}
	}
}

// ServeHTTP handles the incoming SSE HTTP request.
func (b *SSEBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	clientChan := make(chan string, 16)

	b.clientsMu.Lock()
	b.clients[clientChan] = true
	b.clientsMu.Unlock()

	defer func() {
		b.clientsMu.Lock()
		delete(b.clients, clientChan)
		b.clientsMu.Unlock()
		close(clientChan)
	}()

	// Send initial ping
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	notify := r.Context().Done()
	for {
		select {
		case <-notify:
			return
		case <-b.closed:
			return
		case msg, ok := <-clientChan:
			if !ok {
				return
			}
			fmt.Fprint(w, msg)
			flusher.Flush()
		}
	}
}
