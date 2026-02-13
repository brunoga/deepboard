package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Backend struct {
	URL          *url.URL
	ReverseProxy *httputil.ReverseProxy
	Alive        bool
	mux          sync.RWMutex
}

func (b *Backend) SetAlive(alive bool) {
	b.mux.Lock()
	b.Alive = alive
	b.mux.Unlock()
}

func (b *Backend) IsAlive() bool {
	b.mux.RLock()
	alive := b.Alive
	b.mux.RUnlock()
	return alive
}

type ServerPool struct {
	backends []*Backend
	current  uint64
	mux      sync.RWMutex
}

func (s *ServerPool) AddBackend(backend *Backend) {
	s.mux.Lock()
	defer s.mux.Unlock()
	s.backends = append(s.backends, backend)
}

func (s *ServerPool) NextIndex() int {
	return int(atomic.AddUint64(&s.current, uint64(1)) % uint64(len(s.backends)))
}

func (s *ServerPool) GetNextValidBackend() *Backend {
	s.mux.RLock()
	defer s.mux.RUnlock()
	if len(s.backends) == 0 {
		return nil
	}
	for i := 0; i < len(s.backends); i++ {
		idx := s.NextIndex()
		if s.backends[idx].IsAlive() {
			return s.backends[idx]
		}
	}
	return nil
}

func (s *ServerPool) GetBackendByURL(u string) *Backend {
	s.mux.RLock()
	defer s.mux.RUnlock()
	for _, b := range s.backends {
		if b.URL.String() == u {
			return b
		}
	}
	return nil
}

var serverPool ServerPool

const stickyCookieName = "SERVERID"

func lbHandler(w http.ResponseWriter, r *http.Request) {
	var backend *Backend

	log.Printf("Request: %s %s [Upgrade: %s]", r.Method, r.URL.Path, r.Header.Get("Upgrade"))

	// Check for sticky cookie
	if cookie, err := r.Cookie(stickyCookieName); err == nil {
		backend = serverPool.GetBackendByURL(cookie.Value)
		if backend != nil && !backend.IsAlive() {
			backend = nil
		}
	}

	if backend == nil {
		backend = serverPool.GetNextValidBackend()
		if backend == nil {
			http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
			return
		}
		// Set sticky cookie
		http.SetCookie(w, &http.Cookie{
			Name:  stickyCookieName,
			Value: backend.URL.String(),
			Path:  "/",
		})
	}

	if strings.ToLower(r.Header.Get("Upgrade")) == "websocket" {
		log.Printf("Upgrading to WebSocket for backend: %s", backend.URL.String())
	}

	backend.ReverseProxy.ServeHTTP(w, r)
}

func main() {
	backendStr := os.Getenv("BACKENDS")
	if backendStr != "" {
		backendURLs := strings.Split(backendStr, ",")
		for _, u := range backendURLs {
			target, err := url.Parse(u)
			if err != nil {
				log.Fatal(err)
			}
			addBackend(target)
		}
	}

	port := os.Getenv("PROXY_PORT")
	if port == "" {
		port = "9000"
	}

	server := http.Server{
		Addr:    ":" + port,
		Handler: http.HandlerFunc(lbHandler),
	}

	go healthCheck()

	log.Printf("Load Balancer started at :%s", port)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func healthCheck() {
	for {
		// Dynamic discovery if DISCOVERY_SERVICE is set
		serviceName := os.Getenv("DISCOVERY_SERVICE")
		if serviceName != "" {
			refreshBackends(serviceName)
		}

		serverPool.mux.RLock()
		backends := make([]*Backend, len(serverPool.backends))
		copy(backends, serverPool.backends)
		serverPool.mux.RUnlock()

		for _, b := range backends {
			alive := isBackendAlive(b.URL)
			b.SetAlive(alive)
		}
		time.Sleep(10 * time.Second)
	}
}

func refreshBackends(serviceName string) {
	_, addrs, err := net.LookupSRV("", "", serviceName)
	if err != nil {
		// Fallback to A record lookup if SRV fails (common in simple Docker DNS)
		ips, err := net.LookupIP(serviceName)
		if err != nil {
			return
		}

		for _, ip := range ips {
			u := &url.URL{
				Scheme: "http",
				Host:   fmt.Sprintf("%s:8080", ip.String()),
			}
			if serverPool.GetBackendByURL(u.String()) == nil {
				addBackend(u)
			}
		}
		return
	}

	for _, addr := range addrs {
		u := &url.URL{
			Scheme: "http",
			Host:   fmt.Sprintf("%s:%d", addr.Target, addr.Port),
		}
		if serverPool.GetBackendByURL(u.String()) == nil {
			addBackend(u)
		}
	}
}

func addBackend(target *url.URL) {
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = target.Host
	}

	backend := &Backend{
		URL:          target,
		ReverseProxy: proxy,
		Alive:        true,
	}
	serverPool.AddBackend(backend)
	log.Printf("Added new backend: %s", target)
}

func isBackendAlive(u *url.URL) bool {
	timeout := 2 * time.Second
	conn, err := net.DialTimeout("tcp", u.Host, timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
