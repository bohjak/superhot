package main

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
)

type connection struct {
	writer  http.ResponseWriter
	flusher http.Flusher
}

type broker struct {
	connections      map[string]*connection
	connectionsMutex sync.RWMutex
}

// subscribes the requestee to the event stream
func (b *broker) handleConnection(rw http.ResponseWriter, req *http.Request) {
	// if I understand this correctly, this basically saves the ResponseWriter as a flusher if it implements that interface
	flusher, ok := rw.(http.Flusher)
	if !ok {
		http.Error(rw, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// register new connection
	b.connectionsMutex.Lock()
	b.connections[req.RemoteAddr] = &connection{
		writer:  rw,
		flusher: flusher,
	}
	b.connectionsMutex.Unlock()

	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	rw.Header().Set("Connection", "keep-alive")
	rw.Header().Set("Access-Control-Allow-Origin", "*")

	// wait for the connection to be closed, then remove it
	<-req.Context().Done()
	b.removeConnection(req.RemoteAddr)
}

func (b *broker) removeConnection(key string) {
	b.connectionsMutex.Lock()
	delete(b.connections, key)
	b.connectionsMutex.Unlock()
}

// sends the reload event to all subscribed connections
func (b *broker) sendReload() {
	b.connectionsMutex.RLock()
	defer b.connectionsMutex.RUnlock()

	// data is mandatory and named events are broken
	msgBytes := []byte("data: reload\n\n")
	for client, connection := range b.connections {
		_, err := connection.writer.Write(msgBytes)
		if err != nil {
			b.removeConnection(client)
			continue
		}

		connection.flusher.Flush()
	}
}

// basically just a wrapper for broker.sendReload()
func (b *broker) handleReloadReq(res http.ResponseWriter, req *http.Request) {
	b.sendReload()
}

type modifyingResponseWriter struct {
	http.ResponseWriter
	buf *bytes.Buffer
}

func (m *modifyingResponseWriter) Write(b []byte) (int, error) {
	return m.buf.Write(b)
}

func modifyHTMLMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mrw := &modifyingResponseWriter{
			ResponseWriter: w,
			buf:            &bytes.Buffer{},
		}

		next.ServeHTTP(mrw, r)

		contentType := w.Header().Get("Content-Type")
		if strings.HasPrefix(contentType, "text/html") {
			// Modify the HTML content here
			modifiedContent := strings.Replace(mrw.buf.String(), "</head>", "<script type=\"text/javascript\">new EventSource(\"sse\").onmessage=()=>window.location.reload()</script></head>", 1)

			// Write the modified content back to the original ResponseWriter
			w.Header().Set("Content-Length", strconv.Itoa(len(modifiedContent)))
			w.Write([]byte(modifiedContent))
		} else {
			// For non-HTML content, write the original response
			w.Write(mrw.buf.Bytes())
		}
	})
}

func main() {
	dir := "."
	port := "3000"
	if len(os.Args) >= 2 {
		dir = os.Args[1]
	}
	if len(os.Args) >= 3 {
		port = os.Args[2]
	}

	// serve the current directory at '/'
	fs := http.FileServer(http.Dir(dir))
	http.Handle("/", fs)

	broker := broker{connections: map[string]*connection{}}
	// endpoint for subscribing new connections to the event stream
	http.HandleFunc("/sse", broker.handleConnection)
	// endpoint for triggering sending the reload event
	http.HandleFunc("/sse/reload", broker.handleReloadReq)

	fmt.Printf("starting on http://localhost:%s\n", port)
	fmt.Printf("send requests to http://localhost:%s/sse/reload to trigger reloads", port)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%s", port), nil))
}
