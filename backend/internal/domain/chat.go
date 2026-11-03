package domain

type Conversation struct {
	ID           string `json:"id"`
	ProductID    string `json:"product_id"`
	ProductTitle string `json:"product_title"`
	Image        string `json:"image"`
	PeerID       string `json:"peer_id"`
	PeerName     string `json:"peer_name"`
	PeerOnline   bool   `json:"peer_online"`
	Seq          int64  `json:"seq"`
	Unread       int64  `json:"unread"`
	LastMessage  string `json:"last_message"`
	UpdatedAt    int64  `json:"updated_at"`
}

type Message struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	SenderID       string `json:"sender_id"`
	ClientMsgID    string `json:"client_msg_id"`
	Seq            int64  `json:"seq"`
	Content        string `json:"content"`
	CreatedAt      int64  `json:"created_at"`
}

// MessagePage 是增量同步响应。
type MessagePage struct {
	Items   []Message `json:"items"`
	NextSeq int64     `json:"next_seq"`
	HasMore bool      `json:"has_more"`
}

// ConversationCreated 是创建/复用会话的响应。
type ConversationCreated struct {
	ID string `json:"id"`
}

// ReadState 是已读上报结果。
type ReadState struct {
	Read bool `json:"read"`
}
