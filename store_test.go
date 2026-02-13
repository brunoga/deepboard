package main

import (
	"os"
	"path/filepath"
	"testing"
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
	store.Edit(func(bs *BoardState) {
		bs.Board.Columns[0].Cards = append(bs.Board.Columns[0].Cards, Card{
			ID:    "new-card",
			Title: "New Task",
		})
	})

	board := store.GetBoard()
	found := false
	for _, c := range board.Board.Columns[0].Cards {
		if c.ID == "new-card" {
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

	// Clear initial description
	delta0 := s1.Edit(func(bs *BoardState) {
		bs.Board.Columns[0].Cards[0].Description = nil
	})
	s2.ApplyDelta(delta0)

	// Node 1: Insert "Hello "
	delta1 := s1.Edit(func(bs *BoardState) {
		bs.Board.Columns[0].Cards[0].Description = bs.Board.Columns[0].Cards[0].Description.Insert(0, "Hello ", s1.crdt.Clock)
	})
	s2.ApplyDelta(delta1)

	// Node 2: Append "World"
	delta2 := s2.Edit(func(bs *BoardState) {
		desc := bs.Board.Columns[0].Cards[0].Description
		bs.Board.Columns[0].Cards[0].Description = desc.Insert(len(desc.String()), "World", s2.crdt.Clock)
	})
	s1.ApplyDelta(delta2)

	// Verify both see the same result
	finalS1 := s1.GetBoard().Board.Columns[0].Cards[0].Description.String()
	finalS2 := s2.GetBoard().Board.Columns[0].Cards[0].Description.String()

	expected := "Hello World"
	if finalS1 != expected || finalS2 != expected {
		t.Errorf("text sync failed!\nExpected: %s\nNode 1: %s\nNode 2: %s", expected, finalS1, finalS2)
	}
}

func TestStore_ConnectionTracking(t *testing.T) {
	s1, c1 := setupTestStore(t, "conn1", "node-1")
	defer c1()
	s2, c2 := setupTestStore(t, "conn2", "node-2")
	defer c2()

	// 1. Initially 1 node connection (the current node with 0 connections)
	if len(s1.GetBoard().NodeConnections) != 1 {
		t.Errorf("expected 1 node connection initially, got %d", len(s1.GetBoard().NodeConnections))
	}

	// 2. Node 1 subscribes (1 connection)
	sub1 := s1.Subscribe()
	defer s1.Unsubscribe(sub1)

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
	state1 = s1.GetBoard()
	for _, nc := range state1.NodeConnections {
		if nc.NodeID == "node-1" && nc.Count != 0 {
			t.Errorf("expected 0 connections for node-1, got %d", nc.Count)
		}
	}
}
