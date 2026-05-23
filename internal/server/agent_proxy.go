package server

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/neuromaxer/appx/internal/project"
)

// agentServerProxyHandler proxies Appx-authenticated project agent requests to
// a loopback Pi agent-server instance. The browser only sees same-origin Appx
// URLs; cookies and optional agent-server bearer credentials stay server-side.
func agentServerProxyHandler(pm *project.Manager, backendURL string, token string) http.Handler {
	proxy := agentServerReverseProxy(backendURL, token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		projectID := r.PathValue("id")
		if projectID == "" {
			http.Error(w, "project id required", http.StatusBadRequest)
			return
		}
		if _, err := pm.Get(projectID); err != nil {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}

		// SSE streams can live much longer than the server write timeout.
		http.NewResponseController(w).SetWriteDeadline(time.Time{})
		proxy.ServeHTTP(w, r)
	})
}

// agentServerGlobalProxyHandler exposes global runtime resources such as auth
// status. Unlike project session routes, these are tied to the configured
// agent-server process and do not require a project id.
func agentServerGlobalProxyHandler(backendURL string, token string) http.Handler {
	proxy := agentServerReverseProxy(backendURL, token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NewResponseController(w).SetWriteDeadline(time.Time{})
		proxy.ServeHTTP(w, r)
	})
}

func agentServerReverseProxy(backendURL string, token string) http.Handler {
	target, err := url.Parse(backendURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, fmt.Sprintf("invalid agent-server URL %q", backendURL), http.StatusInternalServerError)
		})
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			agentPath := strings.TrimPrefix(req.PathValue("agentPath"), "/")
			req.URL.Path = path.Clean("/v1/" + agentPath)
			req.URL.RawPath = ""
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			req.Header.Del("Cookie")
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("agent-server proxy error path=%s: %v", r.URL.Path, err)
			http.Error(w, "agent-server unavailable", http.StatusBadGateway)
		},
		FlushInterval: -1,
	}

	return proxy
}
