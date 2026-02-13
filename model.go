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
}

type NodeConnection struct {
	NodeID string `deep:"key" json:"nodeID"`
	Count  int    `json:"count"`
}

type Cursor struct {
	ID     string `deep:"key" json:"id"`
	CardID string `json:"cardId"`
	Pos    int    `json:"pos"`
	NodeID string `json:"nodeId"`
}

type Column struct {
	ID    string `deep:"key" json:"id"`
	Title string `json:"title"`
	Cards []Card `json:"cards"`
}

type Board struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Columns []Column `json:"columns"`
}

// BoardState is the top-level structure we wrap in a CRDT.
type BoardState struct {
	Board           Board            `json:"board"`
	NodeConnections []NodeConnection `json:"nodeConnections"`
	Cursors         []Cursor         `json:"cursors"`
}

type WSMessage struct {
	Type   string    `json:"type"`
	Silent bool      `json:"silent,omitempty"`
	Move   *MoveOp   `json:"move,omitempty"`
	TextOp *TextOp   `json:"textOp,omitempty"`
	Delete *DeleteOp `json:"delete,omitempty"`
	Cursor *Cursor   `json:"cursor,omitempty"`
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
				{
					ID:    "todo",
					Title: "To Do",
					Cards: []Card{
						{
							ID:    "card-1",
							Title: "Try Deep Library",
							Description: crdt.Text{
								{ID: hlc.HLC{NodeID: "system"}, Value: "Explore the features of the deep library."},
							},
						},
					},
				},
				{
					ID:    "in-progress",
					Title: "In Progress",
					Cards: []Card{},
				},
				{
					ID:    "done",
					Title: "Done",
					Cards: []Card{},
				},
			},
		},
		NodeConnections: []NodeConnection{},
		Cursors:         []Cursor{},
	}
}
