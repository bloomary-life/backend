package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	telegramDomain "bloomary-api/internal/domain/telegram"
	"bloomary-api/internal/platform/httpclient"
)

const DefaultBaseURL = "https://api.telegram.org"

// API is the Telegram Bot API gate. The bot token is part of every URL, so requests run
// on a context without the request logger and errors never carry the URL.
type API struct {
	client  httpclient.Client
	baseURL string
	token   string
}

var _ telegramDomain.Gate = (*API)(nil)

func New(client httpclient.Client, baseURL, token string) (*API, error) {
	if client == nil {
		return nil, fmt.Errorf("telegram gate: http client is required")
	}

	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("telegram gate: bot token is required")
	}

	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}

	return &API{client: client, baseURL: strings.TrimRight(baseURL, "/"), token: token}, nil
}

type button struct {
	CopyText     *copyText `json:"copy_text,omitempty"`
	Text         string    `json:"text"`
	CallbackData string    `json:"callback_data,omitempty"`
	URL          string    `json:"url,omitempty"`
}

type copyText struct {
	Text string `json:"text"`
}

type replyMarkup struct {
	InlineKeyboard [][]button `json:"inline_keyboard"`
}

type keyboardButton struct {
	Text string `json:"text"`
}

type replyKeyboard struct {
	Keyboard       [][]keyboardButton `json:"keyboard"`
	ResizeKeyboard bool               `json:"resize_keyboard"`
	IsPersistent   bool               `json:"is_persistent"`
}

type apiResponse struct {
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
	OK bool `json:"ok"`
}

type entity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

func entities(list []telegramDomain.Entity) []entity {
	out := make([]entity, 0, len(list))
	for _, e := range list {
		out = append(out, entity{Type: e.Type, Offset: e.Offset, Length: e.Length})
	}
	return out
}

// maxRetryWait: a 429 asking to wait longer than this is returned as an error instead of waited out.
const maxRetryWait = 30 * time.Second

// retryAfterError is Telegram's 429 with its retry_after.
type retryAfterError struct {
	method string
	wait   time.Duration
}

func (e retryAfterError) Error() string {
	return fmt.Sprintf("telegram %s: too many requests, retry after %s", e.method, e.wait)
}

func (a *API) SendMessage(ctx context.Context, message telegramDomain.OutgoingMessage) (int64, error) {
	payload := map[string]any{"chat_id": message.ChatID, "text": message.Text,
		"link_preview_options": map[string]bool{"is_disabled": true}}
	if message.Silent {
		payload["disable_notification"] = true
	}
	if len(message.Entities) > 0 {
		payload["entities"] = entities(message.Entities)
	}
	if len(message.Buttons) > 0 {
		payload["reply_markup"] = markup(message.Buttons)
	} else if message.ForceReply != nil {
		payload["reply_markup"] = map[string]any{"force_reply": true, "input_field_placeholder": message.ForceReply.Placeholder}
	} else if message.RemoveKeyboard {
		payload["reply_markup"] = map[string]bool{"remove_keyboard": true}
	} else if len(message.Keyboard) > 0 {
		keyboard := replyKeyboard{ResizeKeyboard: true, IsPersistent: true}
		for _, row := range message.Keyboard {
			buttons := make([]keyboardButton, 0, len(row))
			for _, text := range row {
				buttons = append(buttons, keyboardButton{Text: text})
			}
			keyboard.Keyboard = append(keyboard.Keyboard, buttons)
		}
		payload["reply_markup"] = keyboard
	}

	var result struct {
		MessageID int64 `json:"message_id"`
	}
	if err := a.callJSON(ctx, "sendMessage", payload, &result); err != nil {
		return 0, err
	}

	return result.MessageID, nil
}

func (a *API) SendPhoto(ctx context.Context, photo telegramDomain.Photo) (int64, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("chat_id", strconv.FormatInt(photo.ChatID, 10))
	if photo.Caption != "" {
		_ = writer.WriteField("caption", photo.Caption)
	}
	if len(photo.Buttons) > 0 {
		keyboard, err := json.Marshal(markup(photo.Buttons))
		if err != nil {
			return 0, err
		}
		_ = writer.WriteField("reply_markup", string(keyboard))
	}

	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="photo"; filename="image"`)
	header.Set("Content-Type", photo.MimeType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return 0, err
	}

	if _, err := part.Write(photo.Data); err != nil {
		return 0, err
	}

	if err := writer.Close(); err != nil {
		return 0, err
	}

	var result struct {
		MessageID int64 `json:"message_id"`
	}
	if err := a.call(ctx, "sendPhoto", writer.FormDataContentType(), &body, &result); err != nil {
		return 0, err
	}

	return result.MessageID, nil
}

// EditMessageText replaces the text and removes the inline keyboard, so a button works once. A photo message (a post
// preview with its post as the caption, BE-026) has no text: then its caption is replaced instead.
func (a *API) EditMessageText(ctx context.Context, chatID, messageID int64, text string) error {
	err := a.callJSON(ctx, "editMessageText", map[string]any{"chat_id": chatID, "message_id": messageID, "text": text,
		"reply_markup": replyMarkup{InlineKeyboard: [][]button{}}}, nil)
	if err == nil {
		return nil
	}
	if r := []rune(text); len(r) > captionRunes {
		text = string(r[:captionRunes-1]) + "…"
	}
	if captionErr := a.callJSON(ctx, "editMessageCaption", map[string]any{"chat_id": chatID, "message_id": messageID, "caption": text,
		"reply_markup": replyMarkup{InlineKeyboard: [][]button{}}}, nil); captionErr != nil {
		return err
	}
	return nil
}

// captionRunes keeps an edited caption under Telegram's 1024-unit caption limit (emoji count twice in UTF-16).
const captionRunes = 500

// SendVideo streams the video as a multipart upload (the master itself, at most 50 MB for a bot).
func (a *API) SendVideo(ctx context.Context, video telegramDomain.Video) (int64, error) {
	fields := map[string]string{"chat_id": strconv.FormatInt(video.ChatID, 10), "supports_streaming": "true"}
	if video.Caption != "" {
		fields["caption"] = video.Caption
	}
	if video.Silent {
		fields["disable_notification"] = "true"
	}
	for name, value := range map[string]int{"duration": video.Seconds, "width": video.Width, "height": video.Height} {
		if value > 0 {
			fields[name] = strconv.Itoa(value)
		}
	}
	if err := jsonFields(fields, video.Entities, video.Buttons, "caption_entities"); err != nil {
		return 0, err
	}
	return a.upload(ctx, "sendVideo", fields, "video", "video.mp4", "video/mp4", video.Body)
}

// SendDocument uploads a small file as a document.
func (a *API) SendDocument(ctx context.Context, document telegramDomain.Document) (int64, error) {
	fields := map[string]string{"chat_id": strconv.FormatInt(document.ChatID, 10)}
	if document.Caption != "" {
		fields["caption"] = document.Caption
	}
	if document.Silent {
		fields["disable_notification"] = "true"
	}
	return a.upload(ctx, "sendDocument", fields, "document", document.FileName, document.MimeType, bytes.NewReader(document.Data))
}

func jsonFields(fields map[string]string, list []telegramDomain.Entity, buttons [][]telegramDomain.Button, entitiesField string) error {
	if len(list) > 0 {
		encoded, err := json.Marshal(entities(list))
		if err != nil {
			return err
		}
		fields[entitiesField] = string(encoded)
	}
	if len(buttons) > 0 {
		encoded, err := json.Marshal(markup(buttons))
		if err != nil {
			return err
		}
		fields["reply_markup"] = string(encoded)
	}
	return nil
}

// upload streams one file part after the fields through a pipe, so a 50 MB video is never held in memory.
func (a *API) upload(ctx context.Context, method string, fields map[string]string, part, fileName, mimeType string, body io.Reader) (int64, error) {
	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	go func() {
		err := func() error {
			for name, value := range fields {
				if err := form.WriteField(name, value); err != nil {
					return err
				}
			}
			header := textproto.MIMEHeader{}
			header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, part, fileName))
			header.Set("Content-Type", mimeType)
			file, err := form.CreatePart(header)
			if err != nil {
				return err
			}
			if _, err := io.Copy(file, body); err != nil {
				return err
			}
			return form.Close()
		}()
		_ = writer.CloseWithError(err)
	}()
	var result struct {
		MessageID int64 `json:"message_id"`
	}
	err := a.call(ctx, method, form.FormDataContentType(), reader, &result)
	_ = reader.Close()
	if err != nil {
		return 0, err
	}
	return result.MessageID, nil
}

// EditMessage replaces the text (or caption), entities and buttons of a message.
func (a *API) EditMessage(ctx context.Context, edit telegramDomain.MessageEdit) error {
	payload := map[string]any{"chat_id": edit.ChatID, "message_id": edit.MessageID, "reply_markup": markup(edit.Buttons)}
	method, entitiesField := "editMessageText", "entities"
	if edit.Caption {
		method, entitiesField = "editMessageCaption", "caption_entities"
		payload["caption"] = edit.Text
	} else {
		payload["text"] = edit.Text
		payload["link_preview_options"] = map[string]bool{"is_disabled": true}
	}
	if len(edit.Entities) > 0 {
		payload[entitiesField] = entities(edit.Entities)
	}
	return a.callJSON(ctx, method, payload, nil)
}

// EditButtons replaces only the inline keyboard.
func (a *API) EditButtons(ctx context.Context, chatID, messageID int64, buttons [][]telegramDomain.Button) error {
	return a.callJSON(ctx, "editMessageReplyMarkup", map[string]any{"chat_id": chatID, "message_id": messageID, "reply_markup": markup(buttons)}, nil)
}

// SetMyCommands sets the command menu shown next to the message field.
func (a *API) SetMyCommands(ctx context.Context, commands []telegramDomain.Command) error {
	list := make([]map[string]string, 0, len(commands))
	for _, c := range commands {
		list = append(list, map[string]string{"command": c.Command, "description": c.Description})
	}

	return a.callJSON(ctx, "setMyCommands", map[string]any{"commands": list}, nil)
}

func (a *API) AnswerCallback(ctx context.Context, callbackID, text string, alert bool) error {
	return a.callJSON(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": callbackID, "text": text, "show_alert": alert}, nil)
}

// DownloadFile resolves the file path with getFile and reads at most maxBytes; the bytes stay in memory.
func (a *API) DownloadFile(ctx context.Context, fileID string, maxBytes int64) ([]byte, error) {
	var file struct {
		FilePath string `json:"file_path"`
		FileSize int64  `json:"file_size"`
	}
	if err := a.callJSON(ctx, "getFile", map[string]string{"file_id": fileID}, &file); err != nil {
		return nil, err
	}

	if file.FilePath == "" {
		return nil, errors.New("telegram getFile: no file path")
	}

	if file.FileSize > maxBytes {
		return nil, telegramDomain.ErrFileTooLarge
	}

	detached, cancel := withoutLogger(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(detached, http.MethodGet, a.baseURL+"/file/bot"+a.token+"/"+file.FilePath, nil)
	if err != nil {
		return nil, errors.New("telegram file download: invalid request")
	}

	resp, err := a.client.Do(req, httpclient.WithPathLabel("/file"))
	if err != nil {
		return nil, errors.New("telegram file download: request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram file download: status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, errors.New("telegram file download: read failed")
	}

	if int64(len(data)) > maxBytes {
		return nil, telegramDomain.ErrFileTooLarge
	}

	return data, nil
}

// callJSON waits out one 429 (retry_after up to maxRetryWait) and sends the request again.
func (a *API) callJSON(ctx context.Context, method string, payload, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	err = a.call(ctx, method, "application/json", bytes.NewReader(body), result)
	var limited retryAfterError
	if !errors.As(err, &limited) || limited.wait > maxRetryWait {
		return err
	}
	timer := time.NewTimer(limited.wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	return a.call(ctx, method, "application/json", bytes.NewReader(body), result)
}

func (a *API) call(ctx context.Context, method, contentType string, body io.Reader, result any) error {
	detached, cancel := withoutLogger(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(detached, http.MethodPost, a.baseURL+"/bot"+a.token+"/"+method, body)
	if err != nil {
		return fmt.Errorf("telegram %s: invalid request", method)
	}

	req.Header.Set("Content-Type", contentType)
	resp, err := a.client.Do(req, httpclient.WithPathLabel("/"+method))
	if err != nil {
		return fmt.Errorf("telegram %s: request failed", method)
	}
	defer resp.Body.Close()
	var decoded apiResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&decoded); err != nil {
		return fmt.Errorf("telegram %s: status %d", method, resp.StatusCode)
	}

	if !decoded.OK {
		switch {
		case strings.Contains(decoded.Description, "message is not modified"):
			return nil
		case strings.Contains(decoded.Description, "message to edit not found"):
			return telegramDomain.ErrMessageGone
		case resp.StatusCode == http.StatusTooManyRequests && decoded.Parameters.RetryAfter > 0:
			return retryAfterError{method: method, wait: time.Duration(decoded.Parameters.RetryAfter) * time.Second}
		}
		return fmt.Errorf("telegram %s: %s", method, decoded.Description)
	}

	if result == nil {
		return nil
	}

	if err := json.Unmarshal(decoded.Result, result); err != nil {
		return fmt.Errorf("telegram %s: decode result: %w", method, err)
	}

	return nil
}

func markup(rows [][]telegramDomain.Button) replyMarkup {
	out := replyMarkup{InlineKeyboard: make([][]button, 0, len(rows))}
	for _, row := range rows {
		converted := make([]button, 0, len(row))
		for _, b := range row {
			item := button{Text: b.Text, CallbackData: b.CallbackData, URL: b.URL}
			if b.CopyText != "" {
				item.CopyText = &copyText{Text: b.CopyText}
			}

			converted = append(converted, item)
		}

		out.InlineKeyboard = append(out.InlineKeyboard, converted)
	}

	return out
}

// withoutLogger keeps the caller's cancellation and deadline but drops context values,
// so the shared HTTP client cannot log a URL that contains the bot token.
func withoutLogger(ctx context.Context) (context.Context, context.CancelFunc) {
	detached, cancel := context.WithCancel(context.Background())
	if deadline, ok := ctx.Deadline(); ok {
		var cancelDeadline context.CancelFunc
		detached, cancelDeadline = context.WithDeadline(detached, deadline)
		prev := cancel
		cancel = func() { cancelDeadline(); prev() }
	}

	stop := context.AfterFunc(ctx, cancel)
	return detached, func() { stop(); cancel() }
}
