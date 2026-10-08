package worker

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPrivateSyncRequiresServiceAuthorization(t *testing.T) {
	for _, token := range []string{"", "Bearer ordinary-device-token", "Bearer wrong-service-secret"} {
		w := &Worker{ActionSecret: "isolated-service-secret"}
		called := false
		handler := w.delegated(func(http.ResponseWriter, *http.Request) { called = true })
		req := httptest.NewRequest("GET", "/internal/v1/sync/info", nil)
		req.Header.Set("Authorization", token)
		out := httptest.NewRecorder()
		handler(out, req)
		if out.Code != 401 || called {
			t.Fatal("a public pairing token acquired private service capabilities")
		}
	}
}
func TestPrivateSyncRejectsUnscopedIdentityBeforeDatabaseAccess(t *testing.T) {
	w := &Worker{ActionSecret: "isolated-service-secret"}
	called := false
	req := httptest.NewRequest("GET", "/internal/v1/sync/info", nil)
	req.Header.Set("Authorization", "Bearer "+w.ActionSecret)
	out := httptest.NewRecorder()
	w.delegated(func(http.ResponseWriter, *http.Request) { called = true })(out, req)
	if out.Code != 400 || called {
		t.Fatal("missing project, connection or device was accepted")
	}
}
