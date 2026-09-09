// Package model defines IM domain models.
package model

// MsgType enumerates message kinds.
type MsgType int

const (
	MsgText MsgType = iota
	MsgImage
	MsgAudio
	MsgVideo
	MsgSystem
)

// Message is a single chat message. Key invariants:
//   - Seq is assigned monotonically by the SERVER per conversation (never client time).
//   - ClientMsgID dedups retries (network resend must not create duplicates).
type Message struct {
	ID          string  `json:"id"`          // server message id (snowflake)
	ClientMsgID string  `json:"client_msg_id"` // client-generated idempotency key
	ConvID      string  `json:"conv_id"`
	SenderID    string  `json:"sender_id"`
	Type        MsgType `json:"type"`
	Content     string  `json:"content"`
	Seq         int64   `json:"seq"`         // per-conversation monotonic, server-assigned
	CreatedAt   int64   `json:"created_at"`
	RecalledAt  int64   `json:"recalled_at,omitempty"`
}

// Conversation is a 1:1 or group conversation. SeqCursor tracks the latest seq issued.
type Conversation struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // "p2p" | "group"
	Name     string `json:"name,omitempty"`
	Members  []string `json:"members"`
	SeqCursor int64  `json:"seq_cursor"` // last assigned seq; next = cursor+1
}
