package main

import (
	"github.com/brunoga/deep/v3/crdt"
	"github.com/brunoga/deep/v3/crdt/hlc"
)

type Card struct {
	ID          string    `deep:"key" json:"id"`
	Title       string    `json:"title"`
	Description crdt.Text `json:"description"`
	Assignee    string    `json:"assignee"`
	ColumnID    string    `json:"columnID"`
	Order       float64   `json:"order"`
}

type NodeConnection struct {
	NodeID string `deep:"key" json:"nodeID"`
	Count  int    `json:"count"`
}

type Column struct {
	ID    string `deep:"key" json:"id"`
	Title string `json:"title"`
}

type Board struct {
	ID      string          `json:"id"`
	Title   string          `json:"title"`
	Columns []Column        `json:"columns"`
	Cards   map[string]Card `json:"cards"`
}

// BoardState is the top-level structure we wrap in a CRDT.
type BoardState struct {
	Board           Board            `json:"board"`
	NodeConnections []NodeConnection `json:"nodeConnections"`
}

type WSMessage struct {
	Type   string    `json:"type"`
	Silent bool      `json:"silent,omitempty"`
	Move   *MoveOp   `json:"move,omitempty"`
	TextOp *TextOp   `json:"textOp,omitempty"`
	Delete *DeleteOp `json:"delete,omitempty"`
}

type MoveOp struct {
	CardID  string `json:"cardId"`
	FromCol string `json:"from"`
	ToCol   string `json:"to"`
	ToIndex int    `json:"toIndex"`
}

type TextOp struct {
	CardID string `json:"cardId"`
	Op     string `json:"op"`
	Pos    int    `json:"pos"`
	Val    string `json:"val"`
	Length int    `json:"length"`
}

type DeleteOp struct {
	CardID string `json:"cardId"`
}

func NewInitialBoard() BoardState {
	return BoardState{
		Board: Board{
			ID:    "main-board",
			Title: "DeepBoard Kanban",
			Columns: []Column{
				{ID: "todo", Title: "To Do"},
				{ID: "in-progress", Title: "In Progress"},
				{ID: "done", Title: "Done"},
			},
			Cards: map[string]Card{
				"card-1": {
					ID:       "card-1",
					Title:    "Try Deep Library",
					ColumnID: "todo",
					Order:    1000,
					Description: crdt.Text{
						{ID: hlc.HLC{NodeID: "system"}, Value: "Explore the features of the deep library."},
					},
				},
			},
		},
		NodeConnections: []NodeConnection{},
	}
}
