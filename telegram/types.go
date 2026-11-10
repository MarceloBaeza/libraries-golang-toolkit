// Package telegram is a small, domain-free toolkit for Telegram bots built on
// the Bot API with long polling:
//
//   - Client: getUpdates, sendMessage, editMessageText, answerCallbackQuery,
//     setMyCommands, getFile and file download (the bot token never appears
//     in returned errors).
//   - Poller: the getUpdates loop with backoff, readiness and panic recovery.
//   - FlowStore / Session: persistent state of guided button forms, with a
//     compare-and-swap revision so an old or repeated button never applies
//     twice (PostgreSQL implementation in the flowpg subpackage).
//   - FlowCallback / ParseFlowCallback: the "f:<flow>:<rev>:<choice>" button
//     data that binds a button to one revision of one flow.
//   - NewLinkToken / HashLinkToken / DeepLink: one-time tokens for
//     "https://t.me/<bot>?start=<token>" account-linking links.
//
// Only the Bot API fields the toolkit's users need are mapped.
package telegram

// Update is an incoming update.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// User is a Telegram user or bot.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

// Chat is a Telegram chat. Type is "private", "group", "supergroup" or
// "channel".
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// ChatTypePrivate is the type of one-to-one chats with the bot.
const ChatTypePrivate = "private"

// Contact is a shared phone contact. When a user shares their own contact
// through a request_contact button, UserID equals the sender's ID; a
// forwarded or typed contact of someone else has a different (or zero)
// UserID, so callers must compare it with Message.From.ID.
type Contact struct {
	PhoneNumber string `json:"phone_number"`
	FirstName   string `json:"first_name,omitempty"`
	UserID      int64  `json:"user_id,omitempty"`
}

// PhotoSize is one size of a photo. Telegram sends several, smallest first.
type PhotoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FileSize     int64  `json:"file_size,omitempty"`
}

// Document is a general file. FileName and MimeType are set by the sender
// and must not be trusted.
type Document struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

// Message is a Telegram message.
type Message struct {
	MessageID int64       `json:"message_id"`
	From      *User       `json:"from,omitempty"`
	Chat      Chat        `json:"chat"`
	Date      int64       `json:"date"`
	Text      string      `json:"text,omitempty"`
	Caption   string      `json:"caption,omitempty"`
	Contact   *Contact    `json:"contact,omitempty"`
	Photo     []PhotoSize `json:"photo,omitempty"`
	Document  *Document   `json:"document,omitempty"`
}

// LargestPhoto returns the biggest size of the message photo, if any.
func (m *Message) LargestPhoto() (PhotoSize, bool) {
	if m == nil || len(m.Photo) == 0 {
		return PhotoSize{}, false
	}
	best := m.Photo[0]
	for _, p := range m.Photo[1:] {
		if p.Width*p.Height > best.Width*best.Height {
			best = p
		}
	}
	return best, true
}

// CallbackQuery is sent when an inline keyboard button is pressed. From is
// the user who pressed it; Data is untrusted input.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

// File is the result of getFile. FilePath is valid for at least one hour.
type File struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size,omitempty"`
	FilePath string `json:"file_path,omitempty"`
}

// InlineKeyboardButton is a button with callback data (max 64 bytes) or a URL.
type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

// InlineKeyboardMarkup is an inline keyboard attached to a message.
type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

// KeyboardButton is a reply-keyboard button. RequestContact asks the client
// to share the user's own phone number (private chats only).
type KeyboardButton struct {
	Text           string `json:"text"`
	RequestContact bool   `json:"request_contact,omitempty"`
}

// ReplyKeyboardMarkup is a custom keyboard shown instead of the letter keys.
type ReplyKeyboardMarkup struct {
	Keyboard        [][]KeyboardButton `json:"keyboard"`
	ResizeKeyboard  bool               `json:"resize_keyboard,omitempty"`
	OneTimeKeyboard bool               `json:"one_time_keyboard,omitempty"`
}

// ReplyKeyboardRemove hides a reply keyboard.
type ReplyKeyboardRemove struct {
	RemoveKeyboard bool `json:"remove_keyboard"`
}

// ContactRequestKeyboard returns a one-button keyboard that shares the
// user's own phone number.
func ContactRequestKeyboard(label string) *ReplyKeyboardMarkup {
	return &ReplyKeyboardMarkup{
		Keyboard:        [][]KeyboardButton{{{Text: label, RequestContact: true}}},
		ResizeKeyboard:  true,
		OneTimeKeyboard: true,
	}
}

// BotCommand is an entry of the bot's command menu.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// SendMessageParams are the arguments of sendMessage. ReplyMarkup accepts
// *InlineKeyboardMarkup, *ReplyKeyboardMarkup or *ReplyKeyboardRemove.
type SendMessageParams struct {
	ChatID      int64  `json:"chat_id"`
	Text        string `json:"text"`
	ParseMode   string `json:"parse_mode,omitempty"`
	ReplyMarkup any    `json:"reply_markup,omitempty"`
}

// EditMessageTextParams are the arguments of editMessageText (only inline
// keyboards can be attached to edited messages).
type EditMessageTextParams struct {
	ChatID      int64                 `json:"chat_id"`
	MessageID   int64                 `json:"message_id"`
	Text        string                `json:"text"`
	ParseMode   string                `json:"parse_mode,omitempty"`
	ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// AnswerCallbackQueryParams are the arguments of answerCallbackQuery.
type AnswerCallbackQueryParams struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text,omitempty"`
}

// GetUpdatesParams are the arguments of getUpdates.
type GetUpdatesParams struct {
	Offset         int64    `json:"offset,omitempty"`
	Limit          int      `json:"limit,omitempty"`
	Timeout        int      `json:"timeout"`
	AllowedUpdates []string `json:"allowed_updates,omitempty"`
}
