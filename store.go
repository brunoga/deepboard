package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/brunoga/deep/v3/crdt"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type Store struct {
	mu     sync.RWMutex
	db     *sql.DB
	crdt   *crdt.CRDT[BoardState]
	subs   map[chan WSMessage]time.Time
	peers  []string
	nodeID string
	lastCount int
}

func NewStore(dbPath string, nodeID string, peers []string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS state (
			id TEXT PRIMARY KEY,
			data BLOB
		);
		CREATE TABLE IF NOT EXISTS patches (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp TEXT,
			patch BLOB,
			summary TEXT
		);
	`)
	if err != nil {
		return nil, err
	}

	s := &Store{
		db:        db,
		subs:      make(map[chan WSMessage]time.Time),
		peers:     peers,
		nodeID:    nodeID,
		lastCount: -1,
	}

	// Load or initialize state
	var data []byte
	err = db.QueryRow("SELECT data FROM state WHERE id = 'latest'").Scan(&data)
	if err == sql.ErrNoRows {
		s.crdt = crdt.NewCRDT(NewInitialBoard(), nodeID)
		s.saveState()
	} else if err != nil {
		return nil, err
	} else {
		s.crdt = crdt.NewCRDT(BoardState{}, nodeID)
		if err := json.Unmarshal(data, s.crdt); err != nil {
			return nil, err
		}
	}

	s.mu.Lock()
	s.updateConnectionsLocked(0)
	s.mu.Unlock()

	go s.connectionManager()

	return s, nil
}

func (s *Store) connectionManager() {
	ticker := time.NewTicker(5 * time.Second)
	for range ticker.C {
		s.mu.Lock()
		now := time.Now()
		changed := false
		for ch, lastSeen := range s.subs {
			if now.Sub(lastSeen) > 30*time.Second {
				delete(s.subs, ch)
				close(ch)
				changed = true
			}
		}
		
		count := len(s.subs)
		if count != s.lastCount || changed {
			s.lastCount = count
			s.updateConnectionsLocked(count)
		}
		s.mu.Unlock()
	}
}

func (s *Store) UpdatePeers(peers []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peers = peers
}

func (s *Store) GetPeers() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p := make([]string, len(s.peers))
	copy(p, s.peers)
	return p
}

func (s *Store) GetBoard() BoardState {
	return s.crdt.View()
}

func (s *Store) ApplyDelta(delta crdt.Delta[BoardState]) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.crdt.ApplyDelta(delta) {
		log.Printf("Applied delta from remote: %s", delta.Patch.Summary())
		s.saveState()
		s.savePatch(delta)
		// Remote updates for connections are silent
		summary := delta.Patch.Summary()
		silent := strings.Contains(summary, "nodeConnections")
		s.Broadcast(WSMessage{Type: "refresh", Silent: silent})
	}
	return nil
}

func (s *Store) Edit(fn func(*BoardState)) crdt.Delta[BoardState] {
	s.mu.Lock()
	defer s.mu.Unlock()

	delta := s.crdt.Edit(fn)
	if delta.Patch != nil {
		s.saveState()
		s.savePatch(delta)
		s.Broadcast(WSMessage{Type: "refresh"})
		go s.syncToPeers(delta)
	}
	return delta
}

func (s *Store) SilentEdit(fn func(*BoardState)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delta := s.crdt.Edit(fn)
	if delta.Patch != nil {
		s.saveState()
		s.Broadcast(WSMessage{Type: "refresh", Silent: true})
		go s.syncToPeers(delta)
	}
}

func (s *Store) syncToPeers(delta crdt.Delta[BoardState]) {
	data, err := json.Marshal(delta)
	if err != nil {
		log.Printf("Failed to marshal delta for sync: %v", err)
		return
	}

	s.mu.RLock()
	currentPeers := make([]string, len(s.peers))
	copy(currentPeers, s.peers)
	s.mu.RUnlock()

	for _, peer := range currentPeers {
		go func(p string) {
			url := fmt.Sprintf("http://%s/api/sync", p)
			resp, err := http.Post(url, "application/json", bytes.NewReader(data))
			if err != nil {
				log.Printf("Failed to sync with peer %s: %v", p, err)
				return
			}
			resp.Body.Close()
		}(peer)
	}
}

func (s *Store) Merge(other *crdt.CRDT[BoardState]) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.crdt.Merge(other) {
		log.Printf("Merged state from remote")
		s.saveState()
		s.Broadcast(WSMessage{Type: "refresh"}) // Merge is always a full refresh
		return true
	}
	return false
}

func (s *Store) GetHistory(limit int) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query("SELECT summary FROM patches ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var history []string
	for rows.Next() {
		var summary string
		if err := rows.Scan(&summary); err == nil {
			history = append(history, summary)
		}
	}
	return history
}

func (s *Store) ClearHistory() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db.Exec("DELETE FROM patches")
	s.Broadcast(WSMessage{Type: "refresh"})
}

func (s *Store) GetHistoryAsDelta() crdt.Delta[BoardState] {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var data []byte
	s.db.QueryRow("SELECT patch FROM patches ORDER BY id DESC LIMIT 1").Scan(&data)
	
	var delta crdt.Delta[BoardState]
	json.Unmarshal(data, &delta)
	return delta
}

func (s *Store) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clear DB
	s.db.Exec("DELETE FROM state")
	s.db.Exec("DELETE FROM patches")

	// Re-initialize CRDT
	s.crdt = crdt.NewCRDT(NewInitialBoard(), s.nodeID)
	s.saveState()

	// Notify everyone
	s.Broadcast(WSMessage{Type: "refresh"})
}

func (s *Store) saveState() {
	data, _ := json.Marshal(s.crdt)
	s.db.Exec("INSERT OR REPLACE INTO state (id, data) VALUES ('latest', ?)", data)
}

func (s *Store) savePatch(delta crdt.Delta[BoardState]) {
	patchData, _ := json.Marshal(delta)
	summary := delta.Patch.Summary()
	log.Printf("Saving patch: %s", summary)
	s.db.Exec("INSERT INTO patches (timestamp, patch, summary) VALUES (?, ?, ?)",
		delta.Timestamp.String(), patchData, summary)
}

func (s *Store) Subscribe() chan WSMessage {
	ch := make(chan WSMessage, 256)
	s.mu.Lock()
	defer s.mu.Unlock()

	s.subs[ch] = time.Now()
	count := len(s.subs)
	s.lastCount = count
	s.updateConnectionsLocked(count)
	return ch
}

func (s *Store) Unsubscribe(ch chan WSMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.subs[ch]; !ok {
		return
	}
	delete(s.subs, ch)
	close(ch)
	
	count := len(s.subs)
	s.lastCount = count
	s.updateConnectionsLocked(count)
}

func (s *Store) Heartbeat(ch chan WSMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs[ch]; ok {
		s.subs[ch] = time.Now()
	}
}

func (s *Store) UpdateConnections(count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastCount = count
	s.updateConnectionsLocked(count)
}

func (s *Store) updateConnectionsLocked(count int) {
	delta := s.crdt.Edit(func(bs *BoardState) {
		found := false
		for i, nc := range bs.NodeConnections {
			if nc.NodeID == s.nodeID {
				bs.NodeConnections[i].Count = count
				found = true
				break
			}
		}
		if !found {
			bs.NodeConnections = append(bs.NodeConnections, NodeConnection{
				NodeID: s.nodeID,
				Count:  count,
			})
		}
	})
	if delta.Patch != nil {
		s.saveState()
		s.Broadcast(WSMessage{Type: "refresh", Silent: true})
		go s.syncToPeers(delta)
	}
}

func (s *Store) AddCard(title string) string {
	id := uuid.New().String()
	s.Edit(func(bs *BoardState) {
		if bs.Board.Cards == nil {
			bs.Board.Cards = make(map[string]Card)
		}
		// Find max order in todo
		maxOrder := 0.0
		for _, c := range bs.Board.Cards {
			if c.ColumnID == "todo" && c.Order > maxOrder {
				maxOrder = c.Order
			}
		}
		bs.Board.Cards[id] = Card{
			ID:          id,
			Title:       title,
			Description: crdt.Text{},
			ColumnID:    "todo",
			Order:       maxOrder + 1000,
		}
	})
	return id
}

func (s *Store) MoveCard(cardID, fromCol, toCol string, toIndex int) {
	s.Edit(func(bs *BoardState) {
		card, ok := bs.Board.Cards[cardID]
		if !ok {
			return
		}

		// Calculate new order
		// Simplified: just put it at the end for now, or between neighbors if we had them.
		// For a real production app, we'd use Fractional Indexing.
		maxOrder := 0.0
		for _, c := range bs.Board.Cards {
			if c.ColumnID == toCol && c.Order > maxOrder {
				maxOrder = c.Order
			}
		}

		card.ColumnID = toCol
		card.Order = maxOrder + 1000
		bs.Board.Cards[cardID] = card
	})
}

func (s *Store) UpdateCardText(cardID, op, val string, pos, length int) {
	s.Edit(func(bs *BoardState) {
		card, ok := bs.Board.Cards[cardID]
		if !ok {
			return
		}
		if op == "insert" {
			card.Description = card.Description.Insert(pos, val, s.crdt.Clock)
		} else if op == "delete" {
			card.Description = card.Description.Delete(pos, length)
		}
		bs.Board.Cards[cardID] = card
	})
}

func (s *Store) DeleteCard(cardID string) {
	s.Edit(func(bs *BoardState) {
		delete(bs.Board.Cards, cardID)
	})
}

func (s *Store) findCardLocked(bs *BoardState, cardID string) (*Card, int, int) {
	// Deprecated but keeping for compatibility if needed elsewhere
	card, ok := bs.Board.Cards[cardID]
	if !ok {
		return nil, -1, -1
	}
	return &card, -1, -1
}

func (s *Store) Broadcast(msg WSMessage) {
	subCount := len(s.subs)
	if subCount > 0 && !msg.Silent {
		log.Printf("Broadcasting refresh to %d subscribers", subCount)
	}

	for ch := range s.subs {
		select {
		case ch <- msg:
		default:
		}
	}
}
