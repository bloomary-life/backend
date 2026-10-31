package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	telegramDomain "bloomary-api/internal/domain/telegram"
	"bloomary-api/internal/platform/httpclient"

	"github.com/stretchr/testify/require"
)

func TestSendMessageSendsInlineKeyboardWithCopyButton(t *testing.T) {
	// Verifies: BE-012
	var path string
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42}}`))
	}))
	defer server.Close()
	client, err := httpclient.NewClient(context.Background())
	require.NoError(t, err)
	api, err := New(client, server.URL, "123:abc")
	require.NoError(t, err)

	id, err := api.SendMessage(context.Background(), telegramDomain.OutgoingMessage{ChatID: 7, Text: "Hi", Buttons: [][]telegramDomain.Button{
		{{Text: "Open post", URL: "https://www.linkedin.com/feed/update/1"}, {Text: "Copy", CopyText: "Great point"}},
		{{Text: "1", CallbackData: "sg:abc:1"}},
	}})

	require.NoError(t, err)
	require.Equal(t, int64(42), id)
	require.Equal(t, "/bot123:abc/sendMessage", path)
	keyboard := payload["reply_markup"].(map[string]any)["inline_keyboard"].([]any)
	first := keyboard[0].([]any)
	require.Equal(t, "https://www.linkedin.com/feed/update/1", first[0].(map[string]any)["url"])
	require.Equal(t, map[string]any{"text": "Great point"}, first[1].(map[string]any)["copy_text"])
	require.Equal(t, "sg:abc:1", keyboard[1].([]any)[0].(map[string]any)["callback_data"])
}

func TestSendMessageSendsPersistentReplyKeyboardAndSetMyCommandsSendsMenu(t *testing.T) {
	// Verifies: BE-018
	var paths []string
	var payloads []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		paths, payloads = append(paths, r.URL.Path), append(payloads, payload)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":43}}`))
	}))
	defer server.Close()
	client, err := httpclient.NewClient(context.Background())
	require.NoError(t, err)
	api, err := New(client, server.URL, "123:abc")
	require.NoError(t, err)

	_, err = api.SendMessage(context.Background(), telegramDomain.OutgoingMessage{ChatID: 7, Text: "Saved", Keyboard: [][]string{{"Planned posts", "Help"}}})
	require.NoError(t, err)
	require.NoError(t, api.SetMyCommands(context.Background(), []telegramDomain.Command{{Command: "planned", Description: "Scheduled posts"}}))

	require.Equal(t, []string{"/bot123:abc/sendMessage", "/bot123:abc/setMyCommands"}, paths)
	markup := payloads[0]["reply_markup"].(map[string]any)
	require.Equal(t, true, markup["is_persistent"])
	require.Equal(t, true, markup["resize_keyboard"])
	require.Equal(t, "Help", markup["keyboard"].([]any)[0].([]any)[1].(map[string]any)["text"])
	require.Equal(t, []any{map[string]any{"command": "planned", "description": "Scheduled posts"}}, payloads[1]["commands"])
}

func TestDownloadFileReadsFileFromBotFilePath(t *testing.T) {
	// Verifies: BE-010
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bot123:abc/getFile":
			body, _ := io.ReadAll(r.Body)
			require.JSONEq(t, `{"file_id":"voice-1"}`, string(body))
			_, _ = w.Write([]byte(`{"ok":true,"result":{"file_path":"voice/file_1.oga","file_size":4}}`))
		case "/file/bot123:abc/voice/file_1.oga":
			_, _ = w.Write([]byte("OggS"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := httpclient.NewClient(context.Background())
	require.NoError(t, err)
	api, err := New(client, server.URL, "123:abc")
	require.NoError(t, err)

	data, err := api.DownloadFile(context.Background(), "voice-1", 1024)

	require.NoError(t, err)
	require.Equal(t, "OggS", string(data))
}

func TestSendPhotoCarriesCaptionAndButtonsAndEditFallsBackToTheCaption(t *testing.T) {
	// Verifies: BE-026
	var paths []string
	var caption, keyboard string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/bot123:abc/sendPhoto":
			require.NoError(t, r.ParseMultipartForm(1<<20))
			caption, keyboard = r.FormValue("caption"), r.FormValue("reply_markup")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":5}}`))
		case "/bot123:abc/editMessageText": // a photo message has no text to edit
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: there is no text in the message to edit"}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	defer server.Close()
	client, err := httpclient.NewClient(context.Background())
	require.NoError(t, err)
	api, err := New(client, server.URL, "123:abc")
	require.NoError(t, err)

	id, err := api.SendPhoto(context.Background(), telegramDomain.Photo{ChatID: 7, Data: []byte("png"), MimeType: "image/png", Caption: "The post",
		Buttons: [][]telegramDomain.Button{{{Text: "📅 Schedule (next slot)", CallbackData: "sn:n-1"}}}})
	require.NoError(t, err)
	require.Equal(t, int64(5), id)
	require.Equal(t, "The post", caption)
	require.Contains(t, keyboard, `"callback_data":"sn:n-1"`)

	require.NoError(t, api.EditMessageText(context.Background(), 7, 5, "Discarded."))
	require.Equal(t, []string{"/bot123:abc/sendPhoto", "/bot123:abc/editMessageText", "/bot123:abc/editMessageCaption"}, paths)
}
