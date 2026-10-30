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
	OK          bool            `json:"ok"`
}

func (a *API) SendMessage(ctx context.Context, message telegramDomain.OutgoingMessage) (int64, error) {
	payload := map[string]any{"chat_id": message.ChatID, "text": message.Text,
		"link_preview_options": map[string]bool{"is_disabled": true}}
	if len(message.Buttons) > 0 {
		payload["reply_markup"] = markup(message.Buttons)
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

// EditMessageText replaces the text and removes the inline keyboard, so a button works once.
func (a *API) EditMessageText(ctx context.Context, chatID, messageID int64, text string) error {
	return a.callJSON(ctx, "editMessageText", map[string]any{"chat_id": chatID, "message_id": messageID, "text": text,
		"reply_markup": replyMarkup{InlineKeyboard: [][]button{}}}, nil)
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

func (a *API) callJSON(ctx context.Context, method string, payload, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
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
