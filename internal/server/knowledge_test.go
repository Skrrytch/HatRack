package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"hatrack/internal/core"
)

func TestKnowledgeAPI(t *testing.T) {
	e := setup(t)
	if resp, _ := e.do("PUT", "/api/config", cfgYAML); resp.StatusCode != 200 {
		t.Fatalf("config: %d", resp.StatusCode)
	}
	tok := e.token
	e.token = ""
	if resp, _ := e.do("GET", "/api/knowledge", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without token: %d", resp.StatusCode)
	}
	e.token = tok
	resp, body := e.do("GET", "/api/knowledge", "")
	if resp.StatusCode != 200 {
		t.Fatalf("with token: %d %s", resp.StatusCode, body)
	}
	var k core.Knowledge
	if err := json.Unmarshal([]byte(body), &k); err != nil {
		t.Fatal(err)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	if resp, _ := e.do("GET", "/api/knowledge", "", "If-None-Match", etag); resp.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match: %d", resp.StatusCode)
	}
}
