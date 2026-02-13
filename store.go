package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/brunoga/deep/v3/crdt"
	_ "modernc.org/sqlite"
)

type Store struct {
	mu     sync.RWMutex
	db     *sql.DB
	crdt   *crdt.CRDT[BoardState]
	subs   map[chan []byte]bool
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
		subs:   make(map[chan []byte]bool),
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

	s.updateConnections(0)

	return s, nil
}

func (s *Store) GetBoard() BoardState {
	return s.crdt.View()
}

func (s *Store) ApplyDelta(delta crdt.Delta[BoardState]) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.crdt.ApplyDelta(delta) {
		s.saveState()
		s.savePatch(delta)
		s.broadcast(&delta)
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
		s.broadcast(&delta)
		go s.syncToPeers(delta)
	}
	return delta
}

func (s *Store) syncToPeers(delta crdt.Delta[BoardState]) {
	data, err := json.Marshal(delta)
	if err != nil {
		return
	}

	for _, peer := range s.peers {
		go func(p string) {
			url := fmt.Sprintf("http://%s/api/sync", p)
			resp, err := http.Post(url, "application/json", bytes.NewReader(data))
			if err != nil {
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
		s.saveState()
		s.broadcast(nil) // Trigger refresh
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

func (s *Store) saveState() {
	data, _ := json.Marshal(s.crdt)
	s.db.Exec("INSERT OR REPLACE INTO state (id, data) VALUES ('latest', ?)", data)
}

func (s *Store) savePatch(delta crdt.Delta[BoardState]) {
	patchData, _ := json.Marshal(delta)
	summary := delta.Patch.Summary()
	s.db.Exec("INSERT INTO patches (timestamp, patch, summary) VALUES (?, ?, ?)", 
		delta.Timestamp.String(), patchData, summary)
}

func (s *Store) Subscribe() chan []byte {
	ch := make(chan []byte, 10)
	s.mu.Lock()
	s.subs[ch] = true
	count := len(s.subs)
	s.mu.Unlock()

	s.updateConnections(count)
	return ch
}

func (s *Store) Unsubscribe(ch chan []byte) {
	s.mu.Lock()
	if _, ok := s.subs[ch]; !ok {
		s.mu.Unlock()
		return
	}
	delete(s.subs, ch)
	close(ch)
	count := len(s.subs)
	s.mu.Unlock()

	s.updateConnections(count)
}

func (s *Store) updateConnections(count int) {
	s.Edit(func(bs *BoardState) {
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
}

func (s *Store) broadcast(delta *crdt.Delta[BoardState]) {
	var data []byte
	if delta != nil {
		data, _ = json.Marshal(delta)
	} else {
		data, _ = json.Marshal(map[string]string{"type": "refresh"})
	}
	for ch := range s.subs {
		select {
		case ch <- data:
		default:
		}
	}
}
