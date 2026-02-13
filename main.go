package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/brunoga/deep/v3/crdt"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

var (
	addr          = flag.String("addr", ":8080", "http service address")
	dbPath        = flag.String("db", "deepboard.db", "path to sqlite database")
	peers         = flag.String("peers", "", "comma-separated list of peer addresses")
	nodeID        = flag.String("node-id", uuid.New().String(), "unique identifier for this node")
	nodeIDFromEnv = flag.Bool("node-id-from-env", false, "use NODE_ID_ENV environment variable for node ID")
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func main() {
	flag.Parse()

	if *nodeIDFromEnv {
		envVar := os.Getenv("NODE_ID_ENV")
		if envVar == "" {
			envVar = "HOSTNAME"
		}
		id := os.Getenv(envVar)
		if id != "" {
			*nodeID = id
		}
	}

	peerList := []string{}
	if *peers != "" {
		peerList = strings.Split(*peers, ",")
	}

	store, err := NewStore(*dbPath, *nodeID, peerList)
	if err != nil {
		log.Fatal(err)
	}

	// Dynamic Peer Discovery if peers look like a single hostname without comma
	if len(peerList) == 1 && !strings.Contains(peerList[0], ":") {
		go discoverPeers(store, peerList[0])
	}

	http.HandleFunc("/", handleIndex(store))
	http.HandleFunc("/ws", handleWS(store))
	http.HandleFunc("/board", handleBoard(store))
	http.HandleFunc("/stats", handleStats(store))
	http.HandleFunc("/history", handleHistory(store))
	http.HandleFunc("/api/add", handleAdd(store))
	http.HandleFunc("/api/sync", handleSync(store))
	http.HandleFunc("/api/state", handleState(store))
	http.HandleFunc("/api/history/clear", handleClearHistory(store))
	http.HandleFunc("/api/connections/cleanup", handleCleanupConnections(store))

	fmt.Printf("DeepBoard starting on http://localhost%s (Node ID: %s)\n", *addr, *nodeID)
	if len(peerList) > 0 {
		fmt.Printf("Peers: %v\n", peerList)
		go startBackgroundSync(store)
		go startConnectionCleanup(store)
	}
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func startConnectionCleanup(s *Store) {
	ticker := time.NewTicker(1 * time.Minute)
	for range ticker.C {
		s.SilentEdit(func(bs *BoardState) {
			s.mu.RLock()
			currentPeers := make([]string, len(s.peers))
			copy(currentPeers, s.peers)
			s.mu.RUnlock()

			peerMap := make(map[string]bool)
			for _, p := range currentPeers {
				// peers are host:port, we want to match NodeID which is usually HOSTNAME
				host := strings.Split(p, ":")[0]
				peerMap[host] = true
			}
			peerMap[*nodeID] = true // Don't delete ourselves

			newConns := []NodeConnection{}
			for _, nc := range bs.NodeConnections {
				// If the node is still in our peer list (or it is us), keep it
				if peerMap[nc.NodeID] {
					newConns = append(newConns, nc)
				}
			}
			bs.NodeConnections = newConns
		})
	}
}

func handleClearHistory(s *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.ClearHistory()
		w.WriteHeader(http.StatusOK)
	}
}

func handleCleanupConnections(s *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.SilentEdit(func(bs *BoardState) {
			s.mu.RLock()
			currentPeers := make([]string, len(s.peers))
			copy(currentPeers, s.peers)
			s.mu.RUnlock()

			peerMap := make(map[string]bool)
			for _, p := range currentPeers {
				host := strings.Split(p, ":")[0]
				peerMap[host] = true
			}
			peerMap[*nodeID] = true

			newConns := []NodeConnection{}
			for _, nc := range bs.NodeConnections {
				if peerMap[nc.NodeID] {
					newConns = append(newConns, nc)
				}
			}
			bs.NodeConnections = newConns
		})
		w.WriteHeader(http.StatusOK)
	}
}

func discoverPeers(s *Store, serviceName string) {
	log.Printf("Starting peer discovery for service: %s", serviceName)
	for {
		ips, err := net.LookupIP(serviceName)
		if err == nil {
			newPeers := []string{}
			for _, ip := range ips {
				peerAddr := fmt.Sprintf("%s:8080", ip.String())
				newPeers = append(newPeers, peerAddr)
			}
			log.Printf("Discovered %d peers: %v", len(newPeers), newPeers)
			s.UpdatePeers(newPeers)
		} else {
			log.Printf("Peer discovery failed: %v", err)
		}
		time.Sleep(30 * time.Second)
	}
}

func handleState(s *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		defer s.mu.RUnlock()
		json.NewEncoder(w).Encode(s.crdt)
	}
}

func startBackgroundSync(s *Store) {
	// Periodic sync every 30 seconds
	ticker := time.NewTicker(30 * time.Second)
	for range ticker.C {
		s.mu.RLock()
		currentPeers := make([]string, len(s.peers))
		copy(currentPeers, s.peers)
		s.mu.RUnlock()

		for _, peer := range currentPeers {
			syncWithPeer(s, peer)
		}
	}
}

func syncWithPeer(s *Store, peer string) {
	url := fmt.Sprintf("http://%s/api/state", peer)
	resp, err := http.Get(url)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	var remoteCRDT crdt.CRDT[BoardState]
	if err := json.NewDecoder(resp.Body).Decode(&remoteCRDT); err != nil {
		return
	}

	s.Merge(&remoteCRDT)
}

func handleSync(s *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var delta crdt.Delta[BoardState]
		if err := json.NewDecoder(r.Body).Decode(&delta); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if delta.Patch == nil {
			// Triggered broadcast from Merge
			s.broadcast(nil)
			w.WriteHeader(http.StatusOK)
			return
		}
		if err := s.ApplyDelta(delta); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func handleIndex(s *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tmpl, err := template.New("index").Parse(indexHTML)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		state := s.GetBoard()
		localCount, totalCount := getConnectionCounts(state)
		data := struct {
			NodeID     string
			Board      Board
			History    []string
			LocalCount int
			TotalCount int
			Cursors    []Cursor
		}{
			NodeID:     *nodeID,
			Board:      state.Board,
			History:    s.GetHistory(15),
			LocalCount: localCount,
			TotalCount: totalCount,
			Cursors:    state.Cursors,
		}
		tmpl.Execute(w, data)
	}
}

func handleStats(s *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state := s.GetBoard()
		localCount, totalCount := getConnectionCounts(state)
		fmt.Fprintf(w, "Local: %d | Total: %d", localCount, totalCount)
	}
}

func getConnectionCounts(state BoardState) (int, int) {
	localCount := 0
	totalCount := 0
	for _, nc := range state.NodeConnections {
		if nc.NodeID == *nodeID {
			localCount = nc.Count
		}
		totalCount += nc.Count
	}
	return localCount, totalCount
}

func handleBoard(s *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tmpl, err := template.New("board").Parse(boardHTML)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		tmpl.Execute(w, s.GetBoard())
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

func handleWS(s *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("WebSocket upgrade failed: %v", err)
			return
		}
		log.Printf("WebSocket connected: %s", r.RemoteAddr)
		defer conn.Close()

		connID := uuid.New().String()
		defer s.removeCursor(connID)

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
			case "cursor":
				if msg.Cursor != nil {
					msg.Cursor.ID = connID
					msg.Cursor.NodeID = *nodeID
					handleCursorInternal(s, msg.Cursor)
				}
			}
		}
	}
}

func handleCursorInternal(s *Store, op *Cursor) {
	s.SilentEdit(func(bs *BoardState) {
		found := false
		for i, c := range bs.Cursors {
			if c.ID == op.ID {
				bs.Cursors[i].CardID = op.CardID
				bs.Cursors[i].Pos = op.Pos
				found = true
				break
			}
		}
		if !found {
			bs.Cursors = append(bs.Cursors, *op)
		}
	})
}

func handleMoveInternal(s *Store, op *MoveOp) {
	log.Printf("Processing move: Card %s from %s to %s (index %d)", op.CardID, op.FromCol, op.ToCol, op.ToIndex)
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
	log.Printf("Processing textOp: Card %s, Op %s, Pos %d", op.CardID, op.Op, op.Pos)
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
	log.Printf("Processing delete: Card %s", op.CardID)
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
{{$cursors := .Cursors}}
{{range .Board.Columns}}
<div class="column">
    <h3>{{.Title}}</h3>
    <div class="card-list" id="col-{{.ID}}" data-col-id="{{.ID}}">
        {{range .Cards}}
        {{$cardID := .ID}}
        <div class="card" data-id="{{.ID}}">
            <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px;">
                <span class="card-title">{{.Title}}</span>
                <button onclick="deleteCard('{{.ID}}')" class="delete-btn">&times;</button>
            </div>
            <textarea class="card-desc" id="desc-{{.ID}}" placeholder="Add a description..."
                      data-last-value="{{.Description.String}}">{{.Description.String}}</textarea>
            <div class="presence-list">
                {{range $cursors}}
                    {{if eq .CardID $cardID}}
                        <span class="presence-tag" title="User {{.ID}}">👤 {{slice .ID 0 4}}</span>
                    {{end}}
                {{end}}
            </div>
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

        .presence-list { display: flex; gap: 4px; margin-top: 4px; flex-wrap: wrap; }
        .presence-tag { font-size: 0.7rem; background: #e0e0e0; padding: 2px 6px; border-radius: 4px; color: #666; }

        .sidebar-header { display: flex; justify-content: space-between; align-items: center; padding: 0 12px; background: #95a5a6; border-radius: 10px 10px 0 0; color: white; }
        .sidebar-header h3 { background: none !important; box-shadow: none !important; margin: 0; }
        .clear-btn { background: #e74c3c; color: white; border: none; border-radius: 4px; padding: 4px 8px; font-size: 0.7rem; cursor: pointer; transition: background 0.2s; }
        .clear-btn:hover { background: #c0392b; }
    </style>
</head>
<body>
    <header>
        <h1>DeepBoard <span style="font-size: 0.8rem; color: #3498db; vertical-align: middle;">(Node: {{.NodeID}})</span></h1>
        <div id="connection-stats" style="color: #bdc3c7; font-size: 0.8rem; margin-left: auto; margin-right: 20px;">
            Local: {{.LocalCount}} | Total: {{.TotalCount}}
            <span onclick="cleanupConnections()" style="cursor: pointer; margin-left: 10px; text-decoration: underline;" title="Force cleanup of stale nodes">🧹</span>
        </div>
        <div class="add-card-form">
            <form action="/api/add" method="POST" style="display: flex; gap: 8px;">
                <input type="text" name="title" placeholder="What needs to be done?" required>
                <button type="submit">Add Task</button>
            </form>
        </div>
    </header>
    
    <div class="main-container">
        <div class="board" id="board">
            ` + "{{with .}}" + boardHTML + "{{end}}" + `
        </div>

        <div class="sidebar">
            <div class="sidebar-header">
                <h3>Activity</h3>
                <button onclick="clearHistory()" class="clear-btn">Clear</button>
            </div>
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
            const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
            socket = new WebSocket(protocol + '//' + window.location.host + '/ws');
            socket.onopen = () => refreshUI();
            socket.onmessage = (e) => {
                if (JSON.parse(e.data).type === 'refresh') refreshUI();
            };
            socket.onclose = () => setTimeout(connect, 1000);
        }

        const lastInputTime = {};
        let refreshTimeout;

        function refreshUI() {
            console.log('Refreshing UI...');
            // Update History & Stats
            fetch('/history').then(r => r.text()).then(html => document.getElementById('history').innerHTML = html);
            fetch('/stats').then(r => r.text()).then(text => document.getElementById('connection-stats').innerHTML = text);
            
            const activeId = document.activeElement && document.activeElement.classList.contains('card-desc') ? document.activeElement.id : null;

            fetch('/board').then(r => r.text()).then(html => {
                const temp = document.createElement('div');
                temp.innerHTML = html;
                
                temp.querySelectorAll('.card-list').forEach(newList => {
                    const oldList = document.getElementById(newList.id);
                    if (!oldList) return;

                    // Update existing cards and add new ones
                    newList.querySelectorAll('.card').forEach(newCard => {
                        const oldCard = oldList.querySelector('[data-id="' + newCard.dataset.id + '"]');
                        if (!oldCard) {
                            oldList.appendChild(newCard);
                        } else {
                            // Update title and presence
                            oldCard.querySelector('.card-title').innerText = newCard.querySelector('.card-title').innerText;
                            oldCard.querySelector('.presence-list').innerHTML = newCard.querySelector('.presence-list').innerHTML;
                            
                            const oldTA = oldCard.querySelector('.card-desc');
                            const newTA = newCard.querySelector('.card-desc');
                            const now = Date.now(), lastTyped = lastInputTime[oldTA.id] || 0;
                            
                            if (oldTA.id === activeId || (now - lastTyped < 1000)) {
                                console.log('Ignoring text update for ' + oldTA.id + ' due to active editing');
                                // Schedule a catch-up refresh
                                clearTimeout(refreshTimeout);
                                refreshTimeout = setTimeout(refreshUI, 1100);
                            } else {
                                if (oldTA.value !== newTA.value) {
                                    oldTA.value = newTA.value;
                                    oldTA.dataset.lastValue = newTA.value;
                                }
                            }
                        }
                    });

                    // Remove cards that are no longer present
                    oldList.querySelectorAll('.card').forEach(oldCard => {
                        if (!newList.querySelector('[data-id="' + oldCard.dataset.id + '"]')) oldCard.remove();
                    });
                });

                initSortable(); initTextareas();
            });
        }

        function deleteCard(cardId) {
            if (confirm('Delete this card?')) {
                socket.send(JSON.stringify({type: 'delete', delete: {cardId}}));
            }
        }

        function clearHistory() {
            if (confirm('Clear activity history?')) {
                fetch('/api/history/clear').then(() => refreshUI());
            }
        }

        function cleanupConnections() {
            fetch('/api/connections/cleanup').then(() => refreshUI());
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
                let inputTimeout;
                let cursorTimeout;
                
                const sendCursor = () => {
                    if (cursorTimeout) return;
                    cursorTimeout = setTimeout(() => {
                        socket.send(JSON.stringify({
                            type: 'cursor',
                            cursor: { cardId: el.id.slice(5), pos: el.selectionStart }
                        }));
                        cursorTimeout = null;
                    }, 200); // Throttle cursors to 5fps
                };

                el.onfocus = sendCursor;
                el.onclick = sendCursor;
                el.onkeyup = (e) => {
                    sendCursor();
                };

                el.oninput = () => {
                    lastInputTime[el.id] = Date.now();
                    sendCursor();
                    clearTimeout(inputTimeout);
                    inputTimeout = setTimeout(() => {
                        const old = el.dataset.lastValue || "", val = el.value;
                        let s = 0; while(s < old.length && s < val.length && old[s] === val[s]) s++;
                        let oe = old.length-1, ne = val.length-1;
                        while(oe >= s && ne >= s && old[oe] === val[ne]) { oe--; ne--; }
                        if (oe >= s) socket.send(JSON.stringify({type:'textOp', textOp:{cardId:el.id.slice(5), op:'delete', pos:s, length:oe-s+1}}));
                        if (ne >= s) socket.send(JSON.stringify({type:'textOp', textOp:{cardId:el.id.slice(5), op:'insert', pos:s, val:val.substring(s, ne+1)}}));
                        el.dataset.lastValue = val;
                    }, 150); // Reduced to 150ms for better feel
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
