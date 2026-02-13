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

	"github.com/brunoga/deep/v3/crdt"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type Store struct {
	mu     sync.RWMutex
	db     *sql.DB
	crdt   *crdt.CRDT[BoardState]
	subs   map[chan WSMessage]bool
	peers  []string
	nodeID string
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
		db:     db,
		subs:   make(map[chan WSMessage]bool),
		peers:  peers,
		nodeID: nodeID,
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

	return s, nil
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
		// Remote updates for cursors/connections are silent
		summary := delta.Patch.Summary()
		silent := strings.Contains(summary, "Cursors") || strings.Contains(summary, "nodeConnections")
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
	ch := make(chan WSMessage, 10)
	s.mu.Lock()
	defer s.mu.Unlock()

	s.subs[ch] = true
	count := len(s.subs)
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
	s.updateConnectionsLocked(count)
}

func (s *Store) UpdateConnections(count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
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

func (s *Store) removeCursor(id string) {
	s.Edit(func(bs *BoardState) {
		newCursors := []Cursor{}
		for _, c := range bs.Cursors {
			if c.ID != id {
				newCursors = append(newCursors, c)
			}
		}
		bs.Cursors = newCursors
	})
}

func (s *Store) SetCursor(cursor Cursor) {
	s.SilentEdit(func(bs *BoardState) {
		found := false
		for i, c := range bs.Cursors {
			if c.ID == cursor.ID {
				bs.Cursors[i].CardID = cursor.CardID
				bs.Cursors[i].Pos = cursor.Pos
				found = true
				break
			}
		}
		if !found {
			bs.Cursors = append(bs.Cursors, cursor)
		}
	})
}

func (s *Store) AddCard(title string) string {
	id := uuid.New().String()
	s.Edit(func(bs *BoardState) {
		bs.Board.Columns[0].Cards = append(bs.Board.Columns[0].Cards, Card{
			ID: id, Title: title,
		})
	})
	return id
}

func (s *Store) MoveCard(cardID, fromCol, toCol string, toIndex int) {
	var card Card
	var found bool

	// 1. Remove from source
	s.Edit(func(bs *BoardState) {
		for ci, col := range bs.Board.Columns {
			if col.ID == fromCol {
				for i, c := range col.Cards {
					if c.ID == cardID {
						card, found = c, true
						bs.Board.Columns[ci].Cards = append(col.Cards[:i], col.Cards[i+1:]...)
						return
					}
				}
			}
		}
	})

	// 2. Add to destination
	if found {
		s.Edit(func(bs *BoardState) {
			for i, col := range bs.Board.Columns {
				if col.ID == toCol {
					idx := toIndex
					if idx > len(col.Cards) {
						idx = len(col.Cards)
					}
					newCards := make([]Card, 0, len(col.Cards)+1)
					newCards = append(newCards, col.Cards[:idx]...)
					newCards = append(newCards, card)
					newCards = append(newCards, col.Cards[idx:]...)
					bs.Board.Columns[i].Cards = newCards
					break
				}
			}
		})
	}
}

func (s *Store) UpdateCardText(cardID, op, val string, pos, length int) {
	s.Edit(func(bs *BoardState) {
		for ci, col := range bs.Board.Columns {
			for ri, card := range col.Cards {
				if card.ID == cardID {
					if op == "insert" {
						bs.Board.Columns[ci].Cards[ri].Description = card.Description.Insert(pos, val, s.crdt.Clock)
					} else if op == "delete" {
						bs.Board.Columns[ci].Cards[ri].Description = card.Description.Delete(pos, length)
					}
					return
				}
			}
		}
	})
}

func (s *Store) DeleteCard(cardID string) {
	s.Edit(func(bs *BoardState) {
		for ci, col := range bs.Board.Columns {
			for ri, card := range col.Cards {
				if card.ID == cardID {
					bs.Board.Columns[ci].Cards = append(col.Cards[:ri], col.Cards[ri+1:]...)
					return
				}
			}
		}
	})
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
