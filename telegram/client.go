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
	"net/url"
	"strconv"
	"strings"
	"time"
)

// APIError is an error reported by the Bot API (ok=false) or an unexpected
// HTTP status. RetryAfter is set on 429 Too Many Requests.
type APIError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
	if e.RetryAfter > 0 {
		s += fmt.Sprintf(" (retry after %s)", e.RetryAfter)
	}
	return s
}

// IsRateLimited reports a 429 error and its retry delay.
func IsRateLimited(err error) (time.Duration, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Code == http.StatusTooManyRequests {
		return apiErr.RetryAfter, true
	}
	return 0, false
}

// Client calls the Bot API. It is safe for concurrent use.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient builds a client. baseURL is normally https://api.telegram.org.
// The http client must not have a global Timeout shorter than the long-poll
// timeout; per-call deadlines are applied via context.
func NewClient(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: httpClient}
}

type envelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// call posts params as JSON to method and decodes result into out.
// Errors never include the request URL, because it contains the token.
func (c *Client) call(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("telegram %s: encode: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram %s: build request failed", method)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, method, out)
}

// do sends req and decodes the Bot API envelope into out.
func (c *Client) do(req *http.Request, method string, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err // drop the URL (and token) from the message
		}
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("telegram %s: read body: %w", method, err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return &APIError{Method: method, Code: resp.StatusCode, Description: "invalid response body"}
	}
	if !env.OK {
		apiErr := &APIError{Method: method, Code: env.ErrorCode, Description: env.Description}
		if apiErr.Code == 0 {
			apiErr.Code = resp.StatusCode
		}
		if env.Parameters != nil && env.Parameters.RetryAfter > 0 {
			apiErr.RetryAfter = time.Duration(env.Parameters.RetryAfter) * time.Second
		}
		return apiErr
	}
	if out != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("telegram %s: decode result: %w", method, err)
		}
	}
	return nil
}

// GetMe validates the token and returns the bot user.
func (c *Client) GetMe(ctx context.Context) (User, error) {
	var u User
	err := c.call(ctx, "getMe", struct{}{}, &u)
	return u, err
}

// GetUpdates long-polls for updates.
func (c *Client) GetUpdates(ctx context.Context, p GetUpdatesParams) ([]Update, error) {
	var ups []Update
	err := c.call(ctx, "getUpdates", p, &ups)
	return ups, err
}

// SendMessage sends a text message and returns it.
func (c *Client) SendMessage(ctx context.Context, p SendMessageParams) (Message, error) {
	var m Message
	err := c.call(ctx, "sendMessage", p, &m)
	return m, err
}

// EditMessageText replaces the text (and keyboard) of a bot message.
func (c *Client) EditMessageText(ctx context.Context, p EditMessageTextParams) error {
	return c.call(ctx, "editMessageText", p, nil)
}

// AnswerCallbackQuery stops the button spinner on the client.
func (c *Client) AnswerCallbackQuery(ctx context.Context, p AnswerCallbackQueryParams) error {
	return c.call(ctx, "answerCallbackQuery", p, nil)
}

// SetMyCommands publishes the command menu.
func (c *Client) SetMyCommands(ctx context.Context, cmds []BotCommand) error {
	return c.call(ctx, "setMyCommands", struct {
		Commands []BotCommand `json:"commands"`
	}{cmds}, nil)
}

// MaxUploadBytes is the Bot API limit for files sent by a bot (50 MB).
const MaxUploadBytes = 50 << 20

// SendDocument uploads a file (multipart/form-data) and sends it as a
// document. Errors never include the request URL, because it contains the
// token.
func (c *Client) SendDocument(ctx context.Context, p SendDocumentParams) (Message, error) {
	const method = "sendDocument"
	var m Message
	if len(p.Data) == 0 {
		return m, fmt.Errorf("telegram %s: empty document", method)
	}
	if len(p.Data) > MaxUploadBytes {
		return m, ErrFileTooLarge
	}
	name := p.FileName
	if name == "" {
		name = "document"
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fields := map[string]string{"chat_id": strconv.FormatInt(p.ChatID, 10)}
	if p.Caption != "" {
		fields["caption"] = p.Caption
	}
	if p.ParseMode != "" {
		fields["parse_mode"] = p.ParseMode
	}
	if p.ReplyMarkup != nil {
		rm, err := json.Marshal(p.ReplyMarkup)
		if err != nil {
			return m, fmt.Errorf("telegram %s: encode reply_markup: %w", method, err)
		}
		fields["reply_markup"] = string(rm)
	}
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return m, fmt.Errorf("telegram %s: encode: %w", method, err)
		}
	}
	part, err := w.CreateFormFile("document", name)
	if err != nil {
		return m, fmt.Errorf("telegram %s: encode: %w", method, err)
	}
	if _, err := part.Write(p.Data); err != nil {
		return m, fmt.Errorf("telegram %s: encode: %w", method, err)
	}
	if err := w.Close(); err != nil {
		return m, fmt.Errorf("telegram %s: encode: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/bot"+c.token+"/"+method, &body)
	if err != nil {
		return m, fmt.Errorf("telegram %s: build request failed", method)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	err = c.do(req, method, &m)
	return m, err
}

// ErrFileTooLarge is returned by DownloadFile when the file exceeds maxBytes,
// and by SendDocument when the document exceeds MaxUploadBytes.
var ErrFileTooLarge = errors.New("telegram: file too large")

// GetFile resolves a file_id into a downloadable path.
func (c *Client) GetFile(ctx context.Context, fileID string) (File, error) {
	var f File
	err := c.call(ctx, "getFile", struct {
		FileID string `json:"file_id"`
	}{fileID}, &f)
	return f, err
}

// DownloadFile fetches a file sent to the bot (getFile + download) and
// refuses anything larger than maxBytes, checking both the size Telegram
// reports and the bytes actually read. Errors never include the download
// URL, because it contains the token.
func (c *Client) DownloadFile(ctx context.Context, fileID string, maxBytes int64) ([]byte, error) {
	f, err := c.GetFile(ctx, fileID)
	if err != nil {
		return nil, err
	}
	if f.FilePath == "" {
		return nil, fmt.Errorf("telegram getFile: empty file_path")
	}
	if maxBytes > 0 && f.FileSize > maxBytes {
		return nil, ErrFileTooLarge
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/file/bot"+c.token+"/"+f.FilePath, nil)
	if err != nil {
		return nil, fmt.Errorf("telegram download: build request failed")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("telegram download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{Method: "download", Code: resp.StatusCode, Description: http.StatusText(resp.StatusCode)}
	}
	limit := maxBytes
	if limit <= 0 {
		limit = 20 << 20 // Bot API download limit
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("telegram download: read body: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, ErrFileTooLarge
	}
	return data, nil
}
