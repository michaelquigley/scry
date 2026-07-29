package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/michaelquigley/df/dd"
)

type mattermostPost struct {
	ChannelID string `dd:"channel_id"`
	Message   string `dd:"message"`
}

func TestMattermostPostsSharedMessageAsBot(t *testing.T) {
	posts := make(chan mattermostPost, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("method: %s", request.Method)
		}
		if request.URL.Path != "/api/v4/posts" {
			t.Errorf("path: %s", request.URL.Path)
		}
		if authorization := request.Header.Get("Authorization"); authorization != "Bearer bot-token" {
			t.Errorf("authorization: %q", authorization)
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content type: %q", request.Header.Get("Content-Type"))
		}
		var post mattermostPost
		if err := dd.BindJSONReader(&post, request.Body, dd.Strict()); err != nil {
			t.Errorf("payload: %v", err)
		}
		posts <- post
		writer.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	transition := testTransition("nas-snapshot")
	notifier := NewMattermost(server.URL, "alerts", "", "bot-token")
	if err := notifier.Notify(context.Background(), transition); err != nil {
		t.Fatal(err)
	}
	post := <-posts
	if post.ChannelID != "alerts" {
		t.Fatalf("channel id: %q", post.ChannelID)
	}
	if post.Message != Message(transition) {
		t.Fatalf("message: %q", post.Message)
	}
}

func TestMattermostReturnsNonSuccessAsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	err := NewMattermost(server.URL, "alerts", "", "bot-token").Notify(
		context.Background(),
		testTransition("job"),
	)
	if err == nil || !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("error: %v", err)
	}
}

func TestMattermostCanceledContextDoesNotPost(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := NewMattermost(server.URL, "alerts", "", "bot-token").Notify(ctx, testTransition("job"))
	if err == nil {
		t.Fatal("canceled notification succeeded")
	}
	if requests.Load() != 0 {
		t.Fatal("canceled notification reached mattermost")
	}
}
