package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const token = "123456:SECRET-TOKEN"

func TestGetUpdatesSendsParamsAndDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot"+token+"/getUpdates" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var p GetUpdatesParams
		_ = json.NewDecoder(r.Body).Decode(&p)
		if p.Offset != 10 || p.Timeout != 30 {
			t.Errorf("unexpected params %+v", p)
		}
		io.WriteString(w, `{"ok":true,"result":[
			{"update_id":10,"message":{"message_id":1,"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"/start"}},
			{"update_id":11,"callback_query":{"id":"cb","from":{"id":42},"data":"m:help","message":{"message_id":2,"chat":{"id":42,"type":"private"}}}}
		]}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, token, srv.Client())
	ups, err := c.GetUpdates(context.Background(), GetUpdatesParams{Offset: 10, Timeout: 30})
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) != 2 || ups[0].Message.Text != "/start" || ups[1].CallbackQuery.From.ID != 42 {
		t.Fatalf("decoded wrong: %+v", ups)
	}
}

func TestAPIErrorWithRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 7","parameters":{"retry_after":7}}`)
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, token, srv.Client()).SendMessage(context.Background(), SendMessageParams{ChatID: 1, Text: "x"})
	d, ok := IsRateLimited(err)
	if !ok || d != 7*time.Second {
		t.Fatalf("expected rate limit 7s, got %v %v", d, err)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("token leaked: %v", err)
	}
}

func TestTransportErrorsDoNotLeakToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // connection refused from now on

	_, err := NewClient(url, token, nil).GetMe(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("token leaked in error: %v", err)
	}
}

func TestInvalidBodyAndHTTPStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, "<html>bad gateway</html>")
	}))
	defer srv.Close()
	err := NewClient(srv.URL, token, srv.Client()).AnswerCallbackQuery(context.Background(), AnswerCallbackQueryParams{CallbackQueryID: "x"})
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("expected 502 api error, got %v", err)
	}
}

func fileServer(t *testing.T, size int64, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bot" + token + "/getFile":
			io.WriteString(w, `{"ok":true,"result":{"file_id":"F","file_size":`+strconv.FormatInt(size, 10)+`,"file_path":"photos/a.jpg"}}`)
		case "/file/bot" + token + "/photos/a.jpg":
			io.WriteString(w, body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestDownloadFile(t *testing.T) {
	srv := fileServer(t, 5, "hello")
	defer srv.Close()
	data, err := NewClient(srv.URL, token, srv.Client()).DownloadFile(context.Background(), "F", 100)
	if err != nil || string(data) != "hello" {
		t.Fatalf("got %q %v", data, err)
	}
}

func TestDownloadFileTooLarge(t *testing.T) {
	// Reported size over the limit: refused before downloading.
	srv := fileServer(t, 1000, "x")
	defer srv.Close()
	if _, err := NewClient(srv.URL, token, srv.Client()).DownloadFile(context.Background(), "F", 100); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge, got %v", err)
	}
	// Reported size lies: the body is still capped.
	srv2 := fileServer(t, 1, strings.Repeat("x", 200))
	defer srv2.Close()
	if _, err := NewClient(srv2.URL, token, srv2.Client()).DownloadFile(context.Background(), "F", 100); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge for lying size, got %v", err)
	}
}

func TestDownloadErrorDoesNotLeakToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			io.WriteString(w, `{"ok":true,"result":{"file_id":"F","file_path":"missing.jpg"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	_, err := NewClient(srv.URL, token, srv.Client()).DownloadFile(context.Background(), "F", 100)
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("expected error without token, got %v", err)
	}
}

func TestSendMessageWithContactKeyboard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), `"request_contact":true`) {
			t.Errorf("missing request_contact: %s", raw)
		}
		io.WriteString(w, `{"ok":true,"result":{"message_id":9,"chat":{"id":1,"type":"private"}}}`)
	}))
	defer srv.Close()
	_, err := NewClient(srv.URL, token, srv.Client()).SendMessage(context.Background(), SendMessageParams{
		ChatID: 1, Text: "x", ReplyMarkup: ContactRequestKeyboard("Compartir"),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestContextCancellation(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release) // runs before srv.Close, unblocking the handler
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := NewClient(srv.URL, token, srv.Client()).GetUpdates(ctx, GetUpdatesParams{Timeout: 50})
	if err == nil {
		t.Fatal("expected context error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("token leaked: %v", err)
	}
}

func TestSendDocumentMultipart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot"+token+"/sendDocument" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("multipart: %v", err)
		}
		if r.FormValue("chat_id") != "42" || r.FormValue("caption") != "Informe" {
			t.Errorf("fields: chat=%q caption=%q", r.FormValue("chat_id"), r.FormValue("caption"))
		}
		if !strings.Contains(r.FormValue("reply_markup"), `"inline_keyboard"`) {
			t.Errorf("reply_markup: %q", r.FormValue("reply_markup"))
		}
		f, h, err := r.FormFile("document")
		if err != nil {
			t.Fatalf("document: %v", err)
		}
		data, _ := io.ReadAll(f)
		if h.Filename != "informe.pdf" || string(data) != "%PDF-1.4 test" {
			t.Errorf("file: %q %q", h.Filename, data)
		}
		io.WriteString(w, `{"ok":true,"result":{"message_id":9,"chat":{"id":42,"type":"private"}}}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, token, srv.Client())
	m, err := c.SendDocument(context.Background(), SendDocumentParams{
		ChatID: 42, FileName: "informe.pdf", Data: []byte("%PDF-1.4 test"), Caption: "Informe",
		ReplyMarkup: &InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{{{Text: "Menú", CallbackData: "m:menu"}}}},
	})
	if err != nil || m.MessageID != 9 {
		t.Fatalf("send: %+v %v", m, err)
	}
}

func TestSendDocumentRejectsEmptyAndHuge(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", token, nil)
	if _, err := c.SendDocument(context.Background(), SendDocumentParams{ChatID: 1}); err == nil {
		t.Fatal("empty document must fail")
	}
	if _, err := c.SendDocument(context.Background(), SendDocumentParams{ChatID: 1, Data: make([]byte, MaxUploadBytes+1)}); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge, got %v", err)
	}
}

func TestSendDocumentErrorDoesNotLeakToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()
	_, err := NewClient(srv.URL, token, nil).SendDocument(context.Background(), SendDocumentParams{ChatID: 1, Data: []byte("x")})
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("expected error without token, got %v", err)
	}
}
