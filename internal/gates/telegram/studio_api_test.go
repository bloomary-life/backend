package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	telegramDomain "bloomary-api/internal/domain/telegram"
	"bloomary-api/internal/platform/httpclient"

	"github.com/stretchr/testify/require"
)

func newTestAPI(t *testing.T, handler http.HandlerFunc) *API {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := httpclient.NewClient(context.Background())
	require.NoError(t, err)
	api, err := New(client, server.URL, "123:abc")
	require.NoError(t, err)
	return api
}

func TestEditsCarryEntitiesAndButtonsAndUnchangedOrRateLimitedEditsAreHandled(t *testing.T) {
	// Verifies: BE-090
	var paths []string
	var payloads []map[string]any
	limited := true
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		paths, payloads = append(paths, r.URL.Path), append(payloads, payload)
		switch {
		case strings.HasSuffix(r.URL.Path, "/editMessageReplyMarkup") && limited:
			limited = false
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"description":"Too Many Requests: retry after 1","parameters":{"retry_after":1}}`))
		case strings.HasSuffix(r.URL.Path, "/editMessageText"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: message is not modified: specified new message content and reply markup are exactly the same"}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":9}}`))
		}
	})
	ctx := context.Background()

	_, err := api.SendMessage(ctx, telegramDomain.OutgoingMessage{ChatID: 7, Text: "Reply with your idea", ForceReply: &telegramDomain.ForceReply{Placeholder: "Your video idea"},
		Entities: []telegramDomain.Entity{{Type: "bold", Offset: 0, Length: 5}}})
	require.NoError(t, err)
	require.NoError(t, api.EditMessage(ctx, telegramDomain.MessageEdit{ChatID: 7, MessageID: 9, Caption: true, Text: "🎬 Title",
		Entities: []telegramDomain.Entity{{Type: "expandable_blockquote", Offset: 3, Length: 5}}, Buttons: [][]telegramDomain.Button{{{Text: "Approve video", CallbackData: "vs:av:r1"}}}}))
	require.NoError(t, api.EditMessage(ctx, telegramDomain.MessageEdit{ChatID: 7, MessageID: 9, Text: "same"}), "message is not modified is not an error")
	require.NoError(t, api.EditButtons(ctx, 7, 9, nil), "one 429 is waited out")

	require.Equal(t, []string{"/bot123:abc/sendMessage", "/bot123:abc/editMessageCaption", "/bot123:abc/editMessageText", "/bot123:abc/editMessageReplyMarkup", "/bot123:abc/editMessageReplyMarkup"}, paths)
	require.Equal(t, map[string]any{"force_reply": true, "input_field_placeholder": "Your video idea"}, payloads[0]["reply_markup"])
	require.Equal(t, []any{map[string]any{"type": "bold", "offset": float64(0), "length": float64(5)}}, payloads[0]["entities"])
	require.Equal(t, "🎬 Title", payloads[1]["caption"])
	require.Equal(t, "expandable_blockquote", payloads[1]["caption_entities"].([]any)[0].(map[string]any)["type"])
	require.Contains(t, payloads[1]["reply_markup"].(map[string]any)["inline_keyboard"].([]any)[0].([]any)[0], "callback_data")
	require.Equal(t, map[string]any{"inline_keyboard": []any{}}, payloads[4]["reply_markup"])
}

func TestEditOfADeletedMessageIsMessageGone(t *testing.T) {
	// Verifies: BE-090
	api := newTestAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: message to edit not found"}`))
	})
	err := api.EditMessage(context.Background(), telegramDomain.MessageEdit{ChatID: 7, MessageID: 9, Text: "x"})
	require.True(t, errors.Is(err, telegramDomain.ErrMessageGone))
}

func TestSendVideoStreamsTheFileWithCaptionEntitiesAndSendDocumentUploadsAFile(t *testing.T) {
	// Verifies: BE-091
	var fields []map[string]string
	var files []string
	api := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseMultipartForm(1<<20))
		got := map[string]string{"path": r.URL.Path}
		for name, values := range r.MultipartForm.Value {
			got[name] = values[0]
		}
		for name, headers := range r.MultipartForm.File {
			file, err := headers[0].Open()
			require.NoError(t, err)
			buf := make([]byte, headers[0].Size)
			_, _ = file.Read(buf)
			files = append(files, name+":"+headers[0].Filename+":"+string(buf))
		}
		fields = append(fields, got)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":11}}`))
	})
	ctx := context.Background()

	id, err := api.SendVideo(ctx, telegramDomain.Video{ChatID: 7, Body: strings.NewReader("synthetic mp4"), Caption: "Title", Seconds: 42, Width: 1080, Height: 1920,
		Entities: []telegramDomain.Entity{{Type: "bold", Length: 5}}, Buttons: [][]telegramDomain.Button{{{Text: "Approve video", CallbackData: "vs:av:r1"}}}, Silent: true})
	require.NoError(t, err)
	require.Equal(t, int64(11), id)
	_, err = api.SendDocument(ctx, telegramDomain.Document{ChatID: 7, Data: []byte("scenario text"), FileName: "scenario.txt", MimeType: "text/plain"})
	require.NoError(t, err)

	require.Equal(t, "/bot123:abc/sendVideo", fields[0]["path"])
	require.Equal(t, "Title", fields[0]["caption"])
	require.Equal(t, "42", fields[0]["duration"])
	require.Equal(t, "1920", fields[0]["height"])
	require.Equal(t, "true", fields[0]["disable_notification"])
	require.JSONEq(t, `[{"type":"bold","offset":0,"length":5}]`, fields[0]["caption_entities"])
	require.Contains(t, fields[0]["reply_markup"], `"callback_data":"vs:av:r1"`)
	require.Equal(t, "/bot123:abc/sendDocument", fields[1]["path"])
	require.Equal(t, []string{"video:video.mp4:synthetic mp4", "document:scenario.txt:scenario text"}, files)
}
