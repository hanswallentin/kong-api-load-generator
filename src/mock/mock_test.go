package mock

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kong/kong-loadgen/src/config"
)

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	cfg := config.Config{
		Users: config.UsersConfig{
			KeyHeader: "apikey",
			Groups:    []config.UserGroup{{Name: "mobile-app", Count: 2, KeyPrefix: "mobile"}},
		},
		Endpoints: []config.Endpoint{
			{Name: "orders", Path: "/orders", Methods: []string{"POST"}, Auth: "key-auth"},
			{Name: "catalog", Path: "/catalog", Methods: []string{"GET"}, Auth: "none"},
		},
	}
	return NewHandler(cfg)
}

func do(t *testing.T, handler http.Handler, method, path string, headers map[string]string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestUnknownPathReturnsNotFound(t *testing.T) {
	if got := do(t, testHandler(t), "GET", "/loadgen-missing-1", nil, "").Code; got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", got)
	}
}

func TestWrongMethodReturnsMethodNotAllowed(t *testing.T) {
	if got := do(t, testHandler(t), "GET", "/orders", map[string]string{"apikey": "mobile-01"}, "").Code; got != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", got)
	}
}

func TestKeyAuthRouteRequiresKnownKey(t *testing.T) {
	handler := testHandler(t)
	headers := map[string]string{"Content-Type": "application/json"}
	if got := do(t, handler, "POST", "/orders", headers, `{"item":"book"}`).Code; got != http.StatusUnauthorized {
		t.Fatalf("missing key status = %d, want 401", got)
	}
	headers["apikey"] = "wrong-key-000001"
	if got := do(t, handler, "POST", "/orders", headers, `{"item":"book"}`).Code; got != http.StatusUnauthorized {
		t.Fatalf("wrong key status = %d, want 401", got)
	}
	headers["apikey"] = "mobile-02"
	if got := do(t, handler, "POST", "/orders", headers, `{"item":"book"}`).Code; got != http.StatusOK {
		t.Fatalf("valid key status = %d, want 200", got)
	}
}

func TestAuthNoneRouteNeedsNoKey(t *testing.T) {
	if got := do(t, testHandler(t), "GET", "/catalog", nil, "").Code; got != http.StatusOK {
		t.Fatalf("status = %d, want 200", got)
	}
}

func TestRequestedErrorStatusIsReturned(t *testing.T) {
	headers := map[string]string{"apikey": "mobile-01", ErrorHeader: "503"}
	if got := do(t, testHandler(t), "POST", "/orders", headers, `{}`).Code; got != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", got)
	}
}

func TestMalformedJSONReturnsBadRequest(t *testing.T) {
	headers := map[string]string{"apikey": "mobile-01", "Content-Type": "application/json"}
	if got := do(t, testHandler(t), "POST", "/orders", headers, `{"loadgen": malformed`).Code; got != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", got)
	}
}

func TestSuccessEchoesHeadersForTheBackend(t *testing.T) {
	headers := map[string]string{"apikey": "mobile-01", "X-Consumer-Username": "mobile-app-01"}
	recorder := do(t, testHandler(t), "POST", "/orders", headers, `{"item":"book"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "mobile-app-01") {
		t.Fatalf("body does not echo the consumer header: %s", recorder.Body.String())
	}
}

func TestListenAddressUsesUpstreamPort(t *testing.T) {
	address, err := ListenAddress("http://localhost:9000")
	if err != nil || address != ":9000" {
		t.Fatalf("ListenAddress = %q, %v; want :9000", address, err)
	}
	if _, err := ListenAddress("http://localhost"); err == nil {
		t.Fatal("expected an error when the upstream URL has no port")
	}
}

func TestIsBuiltinOnlyForLocalMock(t *testing.T) {
	for url, want := range map[string]bool{
		"http://localhost:9000":            true,
		"http://host.docker.internal:9000": true,
		"https://httpbin.konghq.com":       false,
	} {
		if got := IsBuiltin(url); got != want {
			t.Fatalf("IsBuiltin(%q) = %v, want %v", url, got, want)
		}
	}
}
