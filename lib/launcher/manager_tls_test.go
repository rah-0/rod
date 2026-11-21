package launcher

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func managerTestOptions(server *httptest.Server) ManagerClientOptions {
	return ManagerClientOptions{TLSConfig: server.Client().Transport.(*http.Transport).TLSClientConfig}
}

func TestManagedRejectsUnauthenticatedServer(t *testing.T) {
	var requests atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("untrusted listener received manager credentials")
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	plain := httptest.NewServer(handler)
	defer plain.Close()
	if _, err := NewManaged(t.Context(), plain.URL, "private-token"); !errors.Is(err, ErrManagerInsecureTransport) {
		t.Fatalf("plaintext manager = %v", err)
	}
	untrusted := httptest.NewTLSServer(handler)
	defer untrusted.Close()
	if _, err := NewManaged(t.Context(), untrusted.URL, "private-token"); err == nil {
		t.Fatal("untrusted TLS certificate was accepted")
	}
	if _, err := NewManaged(t.Context(), untrusted.URL, "private-token", ManagerClientOptions{TLSConfig: &tls.Config{InsecureSkipVerify: true}}); !errors.Is(err, ErrManagerInsecureTransport) {
		t.Fatalf("disabled certificate verification = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("unauthenticated listener received %d requests", requests.Load())
	}
}
