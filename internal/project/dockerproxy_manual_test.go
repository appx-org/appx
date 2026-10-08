package project

import (
	"net"
	"os"
	"strconv"
	"testing"
	"time"
)

// Manual verification against a real docker-proxy — the exact configuration
// behind issue #8, which no in-process fake can fully stand in for. Skipped
// unless both env vars are set:
//
//	docker run -d --name appx-probe-test -p 127.0.0.1:10500-10502:10500-10502 \
//	  python:3-alpine sh -c 'mkdir -p /www && echo hi > /www/index.html &&
//	    cd /www && python3 -m http.server 10500'
//	APPX_PROBE_SERVING_PORT=10500 APPX_PROBE_EMPTY_PORT=10502 \
//	  go test ./internal/project -run DockerProxy -v
func TestDockerProxy_Manual(t *testing.T) {
	serving := os.Getenv("APPX_PROBE_SERVING_PORT")
	empty := os.Getenv("APPX_PROBE_EMPTY_PORT")
	if serving == "" || empty == "" {
		t.Skip("set APPX_PROBE_SERVING_PORT and APPX_PROBE_EMPTY_PORT (see comment)")
	}

	for _, tc := range []struct {
		name string
		port string
		want bool
	}{
		{"port with a real server behind the proxy", serving, true},
		{"published port with nothing behind it", empty, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port, err := strconv.Atoi(tc.port)
			if err != nil {
				t.Fatal(err)
			}

			// Both ports must accept a bare connection — that is precisely why
			// the old connect-only probe could not tell them apart.
			conn, err := net.DialTimeout("tcp", "127.0.0.1:"+tc.port, 500*time.Millisecond)
			if err != nil {
				t.Fatalf("connect to %s failed, so this is not the docker-proxy case: %v", tc.port, err)
			}
			conn.Close()
			t.Logf("connect-only probe on %d: SUCCEEDED (old behaviour: reported running)", port)

			if got := portServing(port); got != tc.want {
				t.Errorf("portServing(%d) = %v, want %v", port, got, tc.want)
			} else {
				t.Logf("portServing(%d) = %v (correct)", port, got)
			}
		})
	}
}
