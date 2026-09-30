package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testServer(password string) http.Handler {
	static := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>app</title>")}}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// The handlers under test touch only authentication and static files.
	return New(nil, nil, nil, nil, nil, Info{App: "test"}, static, password, log).Handler()
}

func get(h http.Handler, path string, setup func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if setup != nil {
		setup(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestDashboardWithoutPassword(t *testing.T) {
	rec := get(testServer(""), "/dashboard?resolution=800x480&rotate=true", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>app</title>") {
		t.Fatalf("status %d, body %q", rec.Code, rec.Body.String())
	}
}

// A wall display cannot fill in the login form, so /dashboard asks for the
// web password with HTTP basic auth and then issues a normal session.
func TestDashboardBasicAuth(t *testing.T) {
	h := testServer("s3cret")

	rec := get(h, "/dashboard", nil)
	if rec.Code != http.StatusUnauthorized || !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Basic ") {
		t.Fatalf("without credentials: status %d, challenge %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}

	rec = get(h, "/dashboard", func(r *http.Request) { r.SetBasicAuth("display", "wrong") })
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: status %d", rec.Code)
	}

	rec = get(h, "/dashboard", func(r *http.Request) { r.SetBasicAuth("display", "s3cret") })
	if rec.Code != http.StatusOK {
		t.Fatalf("right password: status %d", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookie || !cookies[0].HttpOnly {
		t.Fatalf("expected one HttpOnly session cookie, got %v", cookies)
	}

	// The session alone authorises the page's own requests...
	rec = get(h, "/api/v1/session", func(r *http.Request) { r.AddCookie(cookies[0]) })
	if !strings.Contains(rec.Body.String(), `"authenticated":true`) {
		t.Errorf("session cookie not accepted: %s", rec.Body.String())
	}
	// ...and so do basic credentials on their own.
	rec = get(h, "/api/v1/session", func(r *http.Request) { r.SetBasicAuth("x", "s3cret") })
	if !strings.Contains(rec.Body.String(), `"authenticated":true`) {
		t.Errorf("basic credentials not accepted: %s", rec.Body.String())
	}
	// Anything else stays locked.
	rec = get(h, "/api/v1/devices", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("API without credentials: status %d", rec.Code)
	}
}

func TestClientRoutesFallBackToTheApp(t *testing.T) {
	rec := get(testServer(""), "/some/client/route", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>app</title>") {
		t.Fatalf("status %d", rec.Code)
	}
	if rec := get(testServer(""), "/api/v1/nope", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown API path: status %d", rec.Code)
	}
}
