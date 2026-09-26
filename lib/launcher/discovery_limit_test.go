package launcher

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestResolveURLResponseLimit(t *testing.T) {
	const version = `{"webSocketDebuggerUrl":"ws://localhost:9222/devtools/browser/test"}`
	for _, test := range []struct {
		name    string
		body    string
		endless bool
		wantErr bool
	}{
		{name: "at limit", body: version + strings.Repeat(" ", maxDiscoveryResponse-len(version))},
		{name: "beyond limit", body: version + strings.Repeat(" ", maxDiscoveryResponse-len(version)+1), wantErr: true},
		{name: "endless", endless: true, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !test.endless {
					_, _ = io.WriteString(w, test.body)
					return
				}
				chunk := strings.Repeat(" ", 64<<10)
				for r.Context().Err() == nil {
					if _, err := io.WriteString(w, chunk); err != nil {
						return
					}
				}
			}))
			defer server.Close()
			start := time.Now()
			got, err := ResolveURL(t.Context(), server.URL)
			if !test.wantErr {
				if err != nil || !strings.HasSuffix(got, "/devtools/browser/test") {
					t.Fatalf("ResolveURL = %q, %v", got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "response exceeds 1048576 bytes") {
				t.Fatalf("ResolveURL = %q, %v, want size error", got, err)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("oversized discovery took %s", elapsed)
			}
		})
	}
}
