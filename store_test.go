package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brunoga/deep/v3/crdt"
)

func setupTestStore(t *testing.T, name string, nodeID string) (*Store, func()) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, name+".db")

	store, err := NewStore(dbPath, nodeID, nil)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	cleanup := func() {
		os.Remove(dbPath)
	}

	return store, cleanup
}

func TestStore_Initialization(t *testing.T) {
	store, cleanup := setupTestStore(t, "init", "node-1")
	defer cleanup()

	board := store.GetBoard()
	if board.Board.ID != "main-board" {
		t.Errorf("expected board ID main-board, got %s", board.Board.ID)
	}

	if len(board.Board.Columns) != 3 {
		t.Errorf("expected 3 columns, got %d", len(board.Board.Columns))
	}
}

func TestStore_Edit(t *testing.T) {
	store, cleanup := setupTestStore(t, "edit", "node-1")
	defer cleanup()

	// Add a card
	cardID := store.AddCard("New Task")

	board := store.GetBoard()
	found := false
	for id := range board.Board.Cards {
		if id == cardID {
			found = true
			break
		}
	}

	if !found {
		t.Error("new card not found in board state after Edit")
	}

	// Verify history
	history := store.GetHistory(1)
	if len(history) == 0 {
		t.Error("history should not be empty after Edit")
	}
}

func TestStore_SyncBetweenNodes(t *testing.T) {
	s1, c1 := setupTestStore(t, "node1", "node-1")
	defer c1()
	s2, c2 := setupTestStore(t, "node2", "node-2")
	defer c2()

	// 1. Edit on Node 1
	delta := s1.Edit(func(bs *BoardState) {
		bs.Board.Title = "Updated Title"
	})

	// 2. Apply Delta to Node 2
	err := s2.ApplyDelta(delta)
	if err != nil {
		t.Fatalf("failed to apply delta: %v", err)
	}

	if s2.GetBoard().Board.Title != "Updated Title" {
		t.Errorf("Node 2 state not synced. Got: %s", s2.GetBoard().Board.Title)
	}
}

func TestStore_Merge(t *testing.T) {
	s1, c1 := setupTestStore(t, "merge1", "node-1")
	defer c1()
	s2, c2 := setupTestStore(t, "merge2", "node-2")
	defer c2()

	// Concurrent edits
	s1.Edit(func(bs *BoardState) {
		bs.Board.Columns[1].Title = "In Dev"
	})

	s2.Edit(func(bs *BoardState) {
		bs.Board.Columns[2].Title = "Finished"
	})

	// Merge Node 2 into Node 1
	s1.Merge(s2.crdt)

	board := s1.GetBoard()
	if board.Board.Columns[1].Title != "In Dev" {
		t.Errorf("lost local edit after merge. Got: %s", board.Board.Columns[1].Title)
	}
	if board.Board.Columns[2].Title != "Finished" {
		t.Errorf("failed to merge remote edit. Got: %s", board.Board.Columns[2].Title)
	}
}

func TestStore_TextSynchronization(t *testing.T) {
	s1, c1 := setupTestStore(t, "text1", "node-1")
	defer c1()
	s2, c2 := setupTestStore(t, "text2", "node-2")
	defer c2()

	cardID := "card-1"

	// Clear initial description
	delta0 := s1.Edit(func(bs *BoardState) {
		card := bs.Board.Cards[cardID]
		card.Description = crdt.Text{}
		bs.Board.Cards[cardID] = card
	})
	s2.ApplyDelta(delta0)

	// Node 1: Insert "Hello "
	s1.UpdateCardText(cardID, "insert", "Hello ", 0, 0)
	s2.Merge(s1.crdt)

	// Node 2: Append "World"
	s2.UpdateCardText(cardID, "insert", "World", 6, 0)
	s1.Merge(s2.crdt)

	// Verify both see the same result
	finalS1 := s1.GetBoard().Board.Cards[cardID].Description.String()
	finalS2 := s2.GetBoard().Board.Cards[cardID].Description.String()

	if finalS1 != finalS2 {
		t.Errorf("text sync failed!\nNode 1: %s\nNode 2: %s", finalS1, finalS2)
	}
}

func TestStore_ConnectionTracking(t *testing.T) {
	s1, c1 := setupTestStore(t, "conn1", "node-1")
	defer c1()
	s2, c2 := setupTestStore(t, "conn2", "node-2")
	defer c2()

	time.Sleep(2100 * time.Millisecond) // Wait for initial background sync

	// 1. Initially 1 node connection (the current node with 0 connections)
	if len(s1.GetBoard().NodeConnections) != 1 {
		t.Errorf("expected 1 node connection initially, got %d", len(s1.GetBoard().NodeConnections))
	}

	// 2. Node 1 subscribes (1 connection)
	sub1 := s1.Subscribe()
	defer s1.Unsubscribe(sub1)

	time.Sleep(2100 * time.Millisecond) // Wait for background sync

	state1 := s1.GetBoard()
	if len(state1.NodeConnections) != 1 {
		t.Errorf("expected 1 node connection for node-1, got %d", len(state1.NodeConnections))
	}
	if state1.NodeConnections[0].NodeID != "node-1" || state1.NodeConnections[0].Count != 1 {
		t.Errorf("unexpected node-1 connection state: %+v", state1.NodeConnections[0])
	}

	// 3. Node 2 subscribes (1 connection)
	sub2 := s2.Subscribe()
	defer s2.Unsubscribe(sub2)

	time.Sleep(2100 * time.Millisecond) // Wait for background sync

	// Sync Node 2 state to Node 1 via Merge (to simulate background sync)
	s1.Merge(s2.crdt)

	state1 = s1.GetBoard()
	if len(state1.NodeConnections) != 2 {
		t.Errorf("expected 2 node connections after merge, got %d", len(state1.NodeConnections))
	}

	total := 0
	for _, nc := range state1.NodeConnections {
		total += nc.Count
	}
	if total != 2 {
		t.Errorf("expected total 2 connections, got %d", total)
	}

	// 4. Node 1 unsubscribes
	s1.Unsubscribe(sub1)
	time.Sleep(2100 * time.Millisecond) // Wait for background sync
	state1 = s1.GetBoard()
	for _, nc := range state1.NodeConnections {
		if nc.NodeID == "node-1" && nc.Count != 0 {
			t.Errorf("expected 0 connections for node-1, got %d", nc.Count)
		}
	}
}

func TestStore_CardOperations(t *testing.T) {
	s, cleanup := setupTestStore(t, "ops", "node-1")
	defer cleanup()

	// 1. Add Card
	cardID := s.AddCard("Operation Task")
	board := s.GetBoard()
	// Count cards in todo
	countTodo := 0
	for _, c := range board.Board.Cards {
		if c.ColumnID == "todo" {
			countTodo++
		}
	}
	if countTodo != 2 { // 1 initial + 1 new
		t.Errorf("expected 2 cards in TODO, got %d", countTodo)
	}

	// 2. Move Card (TODO -> In Progress)
	s.MoveCard(cardID, "todo", "in-progress", 0)
	board = s.GetBoard()
	card := board.Board.Cards[cardID]
	if card.ColumnID != "in-progress" {
		t.Errorf("expected card in in-progress, got %s", card.ColumnID)
	}

	// 3. Update Text
	s.UpdateCardText(cardID, "insert", "Detailed description", 0, 0)
	board = s.GetBoard()
	desc := board.Board.Cards[cardID].Description.String()
	if !strings.Contains(desc, "Detailed description") {
		t.Errorf("expected description to contain 'Detailed description', got '%s'", desc)
	}

	// 4. Delete Card
	s.DeleteCard(cardID)
	board = s.GetBoard()
	if _, ok := board.Board.Cards[cardID]; ok {
		t.Error("expected card to be deleted")
	}
}

func TestStore_Reset(t *testing.T) {
	s, cleanup := setupTestStore(t, "reset", "node-1")
	defer cleanup()

	s.AddCard("To be deleted")
	s.Reset()

	board := s.GetBoard()
	if len(board.Board.Cards) != 1 { // Should only have the 1 initial sample card
		t.Errorf("expected only 1 initial card after reset, got %d", len(board.Board.Cards))
	}
}

func TestStore_ConcurrencyAndConvergence(t *testing.T) {
	// Simulate 3 nodes
	s1, c1 := setupTestStore(t, "conv1", "node-1")
	defer c1()
	s2, c2 := setupTestStore(t, "conv2", "node-2")
	defer c2()
	s3, c3 := setupTestStore(t, "conv3", "node-3")
	defer c3()

	// Add a NEW card from Node 1
	cardID := s1.AddCard("Concurrency Test Card")
	s2.Merge(s1.crdt)
	s3.Merge(s1.crdt)

	// 2. Perform concurrent operations directly on CRDTs
	// Node 1: Appends " from 1"
	s1.UpdateCardText(cardID, "insert", " from 1", 0, 0)

	// Node 2: Prepends "Node 2: "
	s2.UpdateCardText(cardID, "insert", "Node 2: ", 0, 0)

	// Node 3: Moves the card to "Done"
	s3.MoveCard(cardID, "todo", "done", 0)

	// 3. Full Mesh Sync using Merge (which preserves history)
	s1.Merge(s2.crdt)
	s1.Merge(s3.crdt)

	s2.Merge(s1.crdt)
	s2.Merge(s3.crdt)

	s3.Merge(s1.crdt)
	s3.Merge(s2.crdt)

	// 4. Verification
	b1 := s1.GetBoard()
	b2 := s2.GetBoard()
	b3 := s3.GetBoard()

	// Check convergence (state must be identical)
	j1, _ := json.Marshal(b1.Board)
	j2, _ := json.Marshal(b2.Board)
	j3, _ := json.Marshal(b3.Board)

	if string(j1) != string(j2) || string(j1) != string(j3) {
		t.Errorf("Boards failed to converge!\nNode 1: %s\nNode 2: %s\nNode 3: %s", string(j1), string(j2), string(j3))
	}

	// Check specific logic: Card should be in "Done" and have BOTH text edits
	card := b1.Board.Cards[cardID]
	if card.ColumnID != "done" {
		t.Errorf("expected card in done, got %s", card.ColumnID)
	}
	txt := card.Description.String()
	// The result should contain parts of both strings (CRDT merging)
	if !strings.Contains(txt, "Node 2:") || !strings.Contains(txt, "from 1") {
		t.Errorf("Merged text is missing edits: '%s'", txt)
	}
}
