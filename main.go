package main

import (
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

var (
	addr   = flag.String("addr", ":8080", "http service address")
	dbPath = flag.String("db", "deepboard.db", "path to sqlite database")
	nodeID = uuid.New().String()
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func main() {
	flag.Parse()

	store, err := NewStore(*dbPath, nodeID)
	if err != nil {
		log.Fatal(err)
	}

	http.HandleFunc("/", handleIndex(store))
	http.HandleFunc("/ws", handleWS(store))
	http.HandleFunc("/board", handleBoard(store))
	http.HandleFunc("/history", handleHistory(store))
	http.HandleFunc("/api/add", handleAdd(store))

	fmt.Printf("DeepBoard starting on http://localhost%s (Node ID: %s)\n", *addr, nodeID)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func handleIndex(s *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tmpl, err := template.New("index").Parse(indexHTML)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		data := struct {
			Board   Board
			History []string
		}{
			Board:   s.GetBoard().Board,
			History: s.GetHistory(15),
		}
		tmpl.Execute(w, data)
	}
}

func handleBoard(s *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tmpl, err := template.New("board").Parse(boardHTML)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		tmpl.Execute(w, s.GetBoard().Board)
	}
}

func handleHistory(s *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		history := s.GetHistory(15)
		for _, h := range history {
			fmt.Fprintf(w, "<div class=\"history-entry\">%s</div>", h)
		}
	}
}

type WSMessage struct {
	Type   string    `json:"type"`
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

func handleWS(s *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		sub := s.Subscribe()
		defer s.Unsubscribe(sub)

		go func() {
			for range sub {
				conn.WriteJSON(WSMessage{Type: "refresh"})
			}
		}()

		for {
			var msg WSMessage
			if err := conn.ReadJSON(&msg); err != nil {
				break
			}

			switch msg.Type {
			case "move":
				if msg.Move != nil {
					handleMoveInternal(s, msg.Move)
				}
			case "textOp":
				if msg.TextOp != nil {
					handleTextOpInternal(s, msg.TextOp)
				}
			case "delete":
				if msg.Delete != nil {
					handleDeleteInternal(s, msg.Delete)
				}
			}
		}
	}
}

func handleMoveInternal(s *Store, op *MoveOp) {
	s.Edit(func(bs *BoardState) {
		var card Card
		var found bool
		var fromColIdx, fromCardIdx int = -1, -1

		for ci, col := range bs.Board.Columns {
			if col.ID == op.FromCol {
				fromColIdx = ci
				for i, c := range col.Cards {
					if c.ID == op.CardID {
						card, fromCardIdx, found = c, i, true
						break
					}
				}
			}
		}

		if found {
			bs.Board.Columns[fromColIdx].Cards = append(
				bs.Board.Columns[fromColIdx].Cards[:fromCardIdx],
				bs.Board.Columns[fromColIdx].Cards[fromCardIdx+1:]...,
			)
			for i, col := range bs.Board.Columns {
				if col.ID == op.ToCol {
					idx := op.ToIndex
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
		}
	})
}

func handleTextOpInternal(s *Store, op *TextOp) {
	s.Edit(func(bs *BoardState) {
		for ci, col := range bs.Board.Columns {
			for ri, card := range col.Cards {
				if card.ID == op.CardID {
					if op.Op == "insert" {
						bs.Board.Columns[ci].Cards[ri].Description = card.Description.Insert(op.Pos, op.Val, s.crdt.Clock)
					} else if op.Op == "delete" {
						bs.Board.Columns[ci].Cards[ri].Description = card.Description.Delete(op.Pos, op.Length)
					}
					return
				}
			}
		}
	})
}

func handleDeleteInternal(s *Store, op *DeleteOp) {
	s.Edit(func(bs *BoardState) {
		for ci, col := range bs.Board.Columns {
			for ri, card := range col.Cards {
				if card.ID == op.CardID {
					bs.Board.Columns[ci].Cards = append(col.Cards[:ri], col.Cards[ri+1:]...)
					return
				}
			}
		}
	})
}

func handleAdd(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		title := r.FormValue("title")
		if title == "" {
			title = "New Task"
		}
		store.Edit(func(bs *BoardState) {
			bs.Board.Columns[0].Cards = append(bs.Board.Columns[0].Cards, Card{
				ID: uuid.New().String(), Title: title,
			})
		})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

const boardHTML = `
{{range .Columns}}
<div class="column">
    <h3>{{.Title}}</h3>
    <div class="card-list" id="col-{{.ID}}" data-col-id="{{.ID}}">
        {{range .Cards}}
        <div class="card" data-id="{{.ID}}">
            <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px;">
                <span class="card-title">{{.Title}}</span>
                <button onclick="deleteCard('{{.ID}}')" class="delete-btn">&times;</button>
            </div>
            <textarea class="card-desc" id="desc-{{.ID}}" placeholder="Add a description..."
                      data-last-value="{{.Description.String}}">{{.Description.String}}</textarea>
        </div>
        {{end}}
    </div>
</div>
{{end}}
`

const indexHTML = `
<!DOCTYPE html>
<html>
<head>
    <title>DeepBoard - Collaborative Kanban</title>
    <script src="https://unpkg.com/htmx.org@1.9.10"></script>
    <script src="https://cdn.jsdelivr.net/npm/sortablejs@1.15.2/Sortable.min.js"></script>
    <style>
        body { font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif; background: #f0f2f5; margin: 0; display: flex; flex-direction: column; height: 100vh; color: #1c1e21; }
        header { background: #2c3e50; color: white; padding: 0.8rem 2rem; display: flex; justify-content: space-between; align-items: center; box-shadow: 0 2px 4px rgba(0,0,0,0.1); }
        header h1 { margin: 0; font-size: 1.5rem; letter-spacing: -0.5px; }
        
        .main-container { display: flex; flex: 1; overflow: hidden; padding: 20px; gap: 20px; }
        .board { display: flex; gap: 20px; flex: 1; overflow-x: auto; align-items: flex-start; }
        
        .column { background: #ebedf0; border-radius: 10px; width: 320px; min-width: 320px; display: flex; flex-direction: column; max-height: 100%; box-shadow: 0 1px 3px rgba(0,0,0,0.1); }
        .column h3 { padding: 12px; margin: 0; text-align: center; color: white; border-radius: 10px 10px 0 0; font-size: 1rem; text-transform: uppercase; letter-spacing: 1px; }
        
        /* Column Header Colors */
        .column:nth-child(1) h3 { background: #3498db; } /* To Do */
        .column:nth-child(2) h3 { background: #f39c12; } /* In Progress */
        .column:nth-child(3) h3 { background: #27ae60; } /* Done */
        
        .card-list { padding: 12px; flex: 1; overflow-y: auto; min-height: 100px; }
        .card { background: white; border-radius: 8px; padding: 12px; margin-bottom: 12px; box-shadow: 0 1px 2px rgba(0,0,0,0.1); cursor: grab; border: 1px solid #e1e4e8; transition: transform 0.1s; }
        .card:hover { border-color: #3498db; }
        .card:active { cursor: grabbing; transform: scale(1.02); }
        .card-title { font-weight: 600; font-size: 0.95rem; color: #2c3e50; }
        
        .delete-btn { background: none; border: none; color: #bdc3c7; cursor: pointer; font-size: 1.4rem; line-height: 1; padding: 0 4px; transition: color 0.2s; }
        .delete-btn:hover { color: #e74c3c; }

        .card-desc { font-size: 0.85rem; color: #5f6368; width: 100%; border: 1px solid transparent; background: #f8f9fa; resize: none; min-height: 60px; margin-top: 8px; border-radius: 4px; padding: 6px; box-sizing: border-box; transition: all 0.2s; }
        .card-desc:focus { background: white; outline: none; border: 1px solid #3498db; color: #1c1e21; box-shadow: 0 0 0 2px rgba(52,152,219,0.1); }
        
        /* Sidebar (History) Styled as a Column */
        .sidebar { background: white; border-radius: 10px; width: 300px; min-width: 300px; display: flex; flex-direction: column; max-height: 100%; box-shadow: 0 1px 3px rgba(0,0,0,0.1); border: 1px solid #e1e4e8; }
        .sidebar h3 { padding: 12px; margin: 0; text-align: center; background: #95a5a6; color: white; border-radius: 10px 10px 0 0; font-size: 1rem; text-transform: uppercase; letter-spacing: 1px; }
        .history-list { padding: 12px; flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: 8px; }
        .history-entry { background: #f8f9fa; border-radius: 6px; padding: 10px; font-size: 0.8rem; color: #4b4f56; border-left: 4px solid #7f8c8d; box-shadow: 0 1px 2px rgba(0,0,0,0.05); word-break: break-all; }

        .add-card-form { display: flex; gap: 8px; }
        .add-card-form input { padding: 8px 12px; border: 1px solid #ddd; border-radius: 6px; flex: 1; font-size: 0.9rem; }
        .add-card-form button { padding: 8px 16px; background: #2ecc71; color: white; border: none; border-radius: 6px; cursor: pointer; font-weight: 600; transition: background 0.2s; }
        .add-card-form button:hover { background: #27ae60; }
    </style>
</head>
<body>
    <header>
        <h1>DeepBoard</h1>
        <div class="add-card-form">
            <form action="/api/add" method="POST" style="display: flex; gap: 8px;">
                <input type="text" name="title" placeholder="What needs to be done?" required>
                <button type="submit">Add Task</button>
            </form>
        </div>
    </header>
    
    <div class="main-container">
        <div class="board" id="board">
            ` + "{{with .Board}}" + boardHTML + "{{end}}" + `
        </div>

        <div class="sidebar">
            <h3>Activity</h3>
            <div class="history-list" id="history">
                {{range .History}}
                <div class="history-entry">{{.}}</div>
                {{end}}
            </div>
        </div>
    </div>

    <script>
        let socket;
        function connect() {
            socket = new WebSocket('ws://' + window.location.host + '/ws');
            socket.onopen = () => refreshUI();
            socket.onmessage = (e) => {
                if (JSON.parse(e.data).type === 'refresh') refreshUI();
            };
            socket.onclose = () => setTimeout(connect, 1000);
        }

        function refreshUI() {
            // Update History
            fetch('/history').then(r => r.text()).then(html => {
                document.getElementById('history').innerHTML = html;
            });
            
            const active = document.activeElement;
            const activeId = active && active.classList.contains('card-desc') ? active.id : null;
            const cursor = activeId ? active.selectionStart : 0;

            // Update Board
            fetch('/board').then(r => r.text()).then(html => {
                document.getElementById('board').innerHTML = html;
                if (activeId) {
                    const el = document.getElementById(activeId);
                    if (el) {
                        el.focus();
                        el.setSelectionRange(cursor, cursor);
                        el.dataset.lastValue = el.value;
                    }
                }
                initSortable(); initTextareas();
            });
        }

        function deleteCard(cardId) {
            if (confirm('Delete this card?')) {
                socket.send(JSON.stringify({type: 'delete', delete: {cardId}}));
            }
        }

        function initSortable() {
            document.querySelectorAll('.card-list').forEach(col => {
                if (col._sortable) col._sortable.destroy();
                col._sortable = new Sortable(col, { group: 'shared', animation: 150, onEnd: e => {
                    const cardId = e.item.dataset.id;
                    const fromColId = e.from.dataset.colId;
                    const toColId = e.to.dataset.colId;
                    const toIndex = e.newIndex;
                    if (fromColId !== toColId || e.oldIndex !== toIndex) {
                        socket.send(JSON.stringify({type:'move', move:{cardId, from:fromColId, to:toColId, toIndex}}));
                    }
                }});
            });
        }

        function initTextareas() {
            document.querySelectorAll('.card-desc').forEach(el => {
                let timeout;
                el.oninput = () => {
                    clearTimeout(timeout);
                    timeout = setTimeout(() => {
                        const old = el.dataset.lastValue || "", val = el.value;
                        let s = 0; while(s < old.length && s < val.length && old[s] === val[s]) s++;
                        let oe = old.length-1, ne = val.length-1;
                        while(oe >= s && ne >= s && old[oe] === val[ne]) { oe--; ne--; }
                        if (oe >= s) socket.send(JSON.stringify({type:'textOp', textOp:{cardId:el.id.slice(5), op:'delete', pos:s, length:oe-s+1}}));
                        if (ne >= s) socket.send(JSON.stringify({type:'textOp', textOp:{cardId:el.id.slice(5), op:'insert', pos:s, val:val.substring(s, ne+1)}}));
                        el.dataset.lastValue = val;
                    }, 100);
                };
            });
        }

        document.addEventListener('DOMContentLoaded', () => {
            connect();
            initSortable();
            initTextareas();
        });
    </script>
</body>
</html>
`
