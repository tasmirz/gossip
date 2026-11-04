package messages

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tasmirz/gossip/pkg/state"
)

// Message represents an inter-agent communication event.
type Message struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`      // e.g. "agent:orch_1" or "orchestrator"
	Role      string    `json:"role"`      // Sender's role
	To        string    `json:"to"`        // "agent:<id>", "role:<role_id>", "all", "*"
	ThreadID  string    `json:"thread_id"` // Thread grouping
	Subject   string    `json:"subject"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	ReadBy    []string  `json:"read_by"` // List of agent IDs who have acknowledged reading
}

// ThreadSummary groups messages for high-level conversation inspection.
type ThreadSummary struct {
	ThreadID     string    `json:"thread_id"`
	Subject      string    `json:"subject"`
	Participants []string  `json:"participants"`
	MessageCount int       `json:"message_count"`
	LastMessage  time.Time `json:"last_message"`
}

// Manager coordinates message logging and inbox retrieval.
type Manager struct {
	store *state.Store
}

// NewManager initializes a Message Manager backed by the given store.
func NewManager(store *state.Store) *Manager {
	return &Manager{store: store}
}

func (m *Manager) messagesFilePath() string {
	return filepath.Join(m.store.Dir(), "messages.jsonl")
}

// SendMessage persists a new message to the append-only ledger.
func (m *Manager) SendMessage(from, role, to, threadID, subject, content string) (*Message, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	now := time.Now()
	msgID := fmt.Sprintf("msg-%d-%04d", now.UnixNano()/1000000, now.Nanosecond()%10000)
	if threadID == "" {
		threadID = fmt.Sprintf("th-%d", now.Unix())
	}
	if to == "" {
		to = "all"
	}

	msg := Message{
		ID:        msgID,
		From:      from,
		Role:      role,
		To:        to,
		ThreadID:  threadID,
		Subject:   subject,
		Content:   content,
		CreatedAt: now,
		ReadBy:    []string{from}, // Author has read their own message
	}

	line, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}

	f, err := os.OpenFile(m.messagesFilePath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open messages.jsonl: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(line, '\n')); err != nil {
		return nil, fmt.Errorf("failed to append message: %w", err)
	}

	return &msg, nil
}

// LoadAllMessages reads all messages from the JSONL log.
func (m *Manager) LoadAllMessages() ([]Message, error) {
	path := m.messagesFilePath()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Message{}, nil
		}
		return nil, err
	}
	defer f.Close()

	var msgs []Message
	scanner := bufio.NewScanner(f)
	// Support large message payloads up to 4MB
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 4*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg Message
		if err := json.Unmarshal(line, &msg); err == nil {
			msgs = append(msgs, msg)
		}
	}

	return msgs, scanner.Err()
}

// ReadInbox retrieves messages intended for a specific agent or role.
func (m *Manager) ReadInbox(agentID, agentRole string, onlyUnread bool, threadID string, limit int) ([]Message, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	allMsgs, err := m.LoadAllMessages()
	if err != nil {
		return nil, err
	}

	normAgentID := strings.TrimPrefix(agentID, "agent:")
	normRole := strings.TrimPrefix(agentRole, "role:")

	var matches []Message
	for i := len(allMsgs) - 1; i >= 0; i-- {
		msg := allMsgs[i]

		// Check thread filter
		if threadID != "" && msg.ThreadID != threadID {
			continue
		}

		// Check recipient match
		isRecipient := false
		target := strings.ToLower(msg.To)

		if target == "all" || target == "*" {
			isRecipient = true
		} else if strings.HasPrefix(target, "role:") {
			reqRole := strings.TrimPrefix(target, "role:")
			if strings.EqualFold(reqRole, normRole) {
				isRecipient = true
			}
		} else if strings.HasPrefix(target, "agent:") {
			reqAgent := strings.TrimPrefix(target, "agent:")
			if strings.EqualFold(reqAgent, normAgentID) {
				isRecipient = true
			}
		} else if strings.EqualFold(target, normAgentID) || strings.EqualFold(target, normRole) {
			isRecipient = true
		}

		if !isRecipient {
			continue
		}

		// Check unread status
		hasRead := false
		for _, reader := range msg.ReadBy {
			if strings.EqualFold(reader, agentID) || strings.EqualFold(reader, normAgentID) {
				hasRead = true
				break
			}
		}

		if onlyUnread && hasRead {
			continue
		}

		matches = append(matches, msg)
		if limit > 0 && len(matches) >= limit {
			break
		}
	}

	// Reverse matches so they are in chronological order
	for i, j := 0, len(matches)-1; i < j; i, j = i+1, j-1 {
		matches[i], matches[j] = matches[j], matches[i]
	}

	return matches, nil
}

// MarkRead marks messages as read by the specified agent.
func (m *Manager) MarkRead(agentID string, messageIDs []string) error {
	unlock, err := m.store.Lock()
	if err != nil {
		return err
	}
	defer unlock()

	allMsgs, err := m.LoadAllMessages()
	if err != nil {
		return err
	}

	targetSet := make(map[string]bool)
	for _, id := range messageIDs {
		targetSet[id] = true
	}

	modified := false
	for i := range allMsgs {
		if targetSet[allMsgs[i].ID] {
			alreadyRead := false
			for _, r := range allMsgs[i].ReadBy {
				if r == agentID {
					alreadyRead = true
					break
				}
			}
			if !alreadyRead {
				allMsgs[i].ReadBy = append(allMsgs[i].ReadBy, agentID)
				modified = true
			}
		}
	}

	if !modified {
		return nil
	}

	// Rewrite ledger atomically
	var b strings.Builder
	for _, msg := range allMsgs {
		data, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		b.Write(data)
		b.WriteByte('\n')
	}

	return m.store.AtomicWriteFile("messages.jsonl", []byte(b.String()))
}

// ListThreads provides an overview of conversation threads.
func (m *Manager) ListThreads(limit int) ([]ThreadSummary, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	allMsgs, err := m.LoadAllMessages()
	if err != nil {
		return nil, err
	}

	threads := make(map[string]*ThreadSummary)
	for _, msg := range allMsgs {
		th, exists := threads[msg.ThreadID]
		if !exists {
			th = &ThreadSummary{
				ThreadID:     msg.ThreadID,
				Subject:      msg.Subject,
				Participants: []string{},
				MessageCount: 0,
				LastMessage:  msg.CreatedAt,
			}
			threads[msg.ThreadID] = th
		}

		th.MessageCount++
		if msg.CreatedAt.After(th.LastMessage) {
			th.LastMessage = msg.CreatedAt
		}

		// Add participant if not present
		found := false
		for _, p := range th.Participants {
			if p == msg.From {
				found = true
				break
			}
		}
		if !found {
			th.Participants = append(th.Participants, msg.From)
		}
	}

	var list []ThreadSummary
	for _, th := range threads {
		list = append(list, *th)
	}

	if limit > 0 && len(list) > limit {
		list = list[:limit]
	}
	return list, nil
}
