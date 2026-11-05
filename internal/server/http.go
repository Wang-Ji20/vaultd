package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// shutdownGrace is how long in-flight tool calls get to finish on shutdown.
const shutdownGrace = 10 * time.Second

// Handler returns all HTTP routes. allowedHost is the only Host header
// accepted, the listener's own host:port.
func (g *Gateway) Handler(allowedHost string) http.Handler {
	endpoint := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return g.mcp }, nil)
	bearer := auth.RequireBearerToken(g.verifyToken, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})
	mux := http.NewServeMux()
	mux.Handle("/mcp", bearer(endpoint))
	mux.HandleFunc("GET /health", g.health)
	if g.admin != nil {
		mux.Handle("/admin", g.admin)
		mux.Handle("/admin/", g.admin)
	}
	return g.refuseWhileDraining(requireHost(allowedHost, mux))
}

// verifyToken accepts tokens of the apps in the current config, so a new or
// revoked token takes effect as soon as the config reloads. The SDK binds each
// session to the app that opened it, so one app cannot use another's session.
func (g *Gateway) verifyToken(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	name, ok := g.store.Get().FindApp(token)
	if !ok {
		return nil, auth.ErrInvalidToken
	}
	return &auth.TokenInfo{UserID: name}, nil
}

func (g *Gateway) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": g.version})
}

// requireHost blocks DNS rebinding. A malicious site can make its own domain
// resolve to 127.0.0.1, but the browser still sends that domain as the Host.
func requireHost(allowed string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != allowed {
			http.Error(w, "forbidden: unexpected Host header; use http://"+allowed, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (g *Gateway) refuseWhileDraining(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g.draining.Load() {
			http.Error(w, "shutting down", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Serve listens on the configured address until ctx ends. If ready is not nil,
// it receives the actual host:port once the server accepts connections.
func (g *Gateway) Serve(ctx context.Context, ready chan<- string) error {
	listen := g.store.Get().Server.Listen
	listener, err := listenLoopback(listen)
	if err != nil {
		return err
	}
	allowed := allowedHostFor(listen, listener.Addr())
	server := &http.Server{
		Handler:           g.Handler(allowed),
		ReadHeaderTimeout: 10 * time.Second,
	}
	failed := make(chan error, 1)
	go func() { failed <- server.Serve(listener) }()
	g.log.Info("listening", "address", "http://"+allowed+"/mcp")
	if ready != nil {
		ready <- allowed
	}
	select {
	case err := <-failed:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	g.shutdown(server)
	if err := <-failed; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

// listenLoopback checks the address the system actually bound, since a name
// such as "localhost" is only known once it has been resolved.
func listenLoopback(listen string) (net.Listener, error) {
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	if addr, ok := listener.Addr().(*net.TCPAddr); !ok || !addr.IP.IsLoopback() {
		listener.Close()
		return nil, fmt.Errorf("listen: refusing %q: not a loopback address", listen)
	}
	return listener, nil
}

// allowedHostFor keeps the host as configured and fills in a system-chosen port.
func allowedHostFor(listen string, addr net.Addr) string {
	host, _, err := net.SplitHostPort(listen)
	tcp, ok := addr.(*net.TCPAddr)
	if err != nil || !ok {
		return addr.String()
	}
	return net.JoinHostPort(host, fmt.Sprint(tcp.Port))
}

// shutdown refuses new requests, lets running tool calls finish, then ends the
// long-lived event streams. http.Server.Shutdown alone would wait out the whole
// grace period on those streams, which stay open for as long as a client does.
func (g *Gateway) shutdown(server *http.Server) {
	g.draining.Store(true)
	g.waitForCalls(shutdownGrace)
	for session := range g.mcp.Sessions() {
		session.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		server.Close()
	}
}

func (g *Gateway) waitForCalls(limit time.Duration) {
	deadline := time.Now().Add(limit)
	for g.inFlight.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
}

// countCalls is the receiving middleware that tracks running tool calls.
func countCalls(counter *atomic.Int64) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			counter.Add(1)
			defer counter.Add(-1)
			return next(ctx, method, req)
		}
	}
}
