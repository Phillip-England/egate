package sdk

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendEmail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/email" || r.Header.Get("Authorization") != "Bearer eg_test" {
			t.Fatalf("unexpected request: path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"from":"from@example.com","to":"to@example.com","subject":"hello","text_body":"body"}` {
			t.Fatalf("body = %s", body)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"Message":"OK"}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "eg_test")
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.SendEmail(context.Background(), Email{From: "from@example.com", To: "to@example.com", Subject: "hello", TextBody: "body"})
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("response=%v err=%v", response, err)
	}
}

func TestSendEmailReturnsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid API key", http.StatusUnauthorized)
	}))
	defer server.Close()
	client, _ := NewClient(server.URL, "bad")
	response, err := client.SendEmail(context.Background(), Email{})
	var apiErr *APIError
	if response == nil || !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("response=%v err=%v", response, err)
	}
}

func TestNewClientRequiresExplicitHost(t *testing.T) {
	for _, host := range []string{"", "localhost:8080", "ftp://example.com"} {
		if _, err := NewClient(host, "key"); err == nil {
			t.Fatalf("NewClient(%q) succeeded", host)
		}
	}
}
