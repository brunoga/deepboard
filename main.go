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
	http.HandleFunc("/api/admin/reset", handleReset(store))

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
		currentPeers := s.GetPeers()
		peerMap := make(map[string]bool)
		for _, p := range currentPeers {
			host := strings.Split(p, ":")[0]
			peerMap[host] = true
		}
		peerMap[*nodeID] = true

		s.SilentEdit(func(bs *BoardState) {
			newConns := []NodeConnection{}
			for _, nc := range bs.NodeConnections {
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
		currentPeers := s.GetPeers()
		peerMap := make(map[string]bool)
		for _, p := range currentPeers {
			host := strings.Split(p, ":")[0]
			peerMap[host] = true
		}
		peerMap[*nodeID] = true

		s.SilentEdit(func(bs *BoardState) {
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

func handleReset(s *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("ADMIN: Resetting board to initial state")
		s.Reset()
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
		currentPeers := s.GetPeers()
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
			s.Broadcast(WSMessage{Type: "refresh"})
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
		tmpl.Execute(w, prepareUIData(s))
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
		tmpl.Execute(w, prepareUIData(s))
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

type UIColumn struct {
	ID    string
	Title string
	Cards []Card
}

type UIData struct {
	NodeID     string
	Columns    []UIColumn
	History    []string
	LocalCount int
	TotalCount int
	Cursors    []Cursor
}

func prepareUIData(s *Store) UIData {
	state := s.GetBoard()
	localCount, totalCount := getConnectionCounts(state)

	uiColumns := make([]UIColumn, len(state.Board.Columns))
	colMap := make(map[string]int)

	for i, col := range state.Board.Columns {
		uiColumns[i] = UIColumn{
			ID:    col.ID,
			Title: col.Title,
			Cards: []Card{},
		}
		colMap[col.ID] = i
	}

	for _, card := range state.Board.Cards {
		if idx, ok := colMap[card.ColumnID]; ok {
			uiColumns[idx].Cards = append(uiColumns[idx].Cards, card)
		}
	}

	// Sort cards in each column by Order
	for i := range uiColumns {
		sortCards(uiColumns[i].Cards)
	}

	return UIData{
		NodeID:     *nodeID,
		Columns:    uiColumns,
		History:    s.GetHistory(15),
		LocalCount: localCount,
		TotalCount: totalCount,
		Cursors:    state.Cursors,
	}
}

func sortCards(cards []Card) {
	for i := 0; i < len(cards); i++ {
		for j := i + 1; j < len(cards); j++ {
			if cards[i].Order > cards[j].Order {
				cards[i], cards[j] = cards[j], cards[i]
			}
		}
	}
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
			for msg := range sub {
				if !msg.Silent {
					log.Printf("Refresh triggered for client %s", connID)
				}
				conn.WriteJSON(msg)
			}
		}()

		for {
			var msg WSMessage
			if err := conn.ReadJSON(&msg); err != nil {
				break
			}
			log.Printf("WS message from %s: type=%s", connID, msg.Type)

			switch msg.Type {
			case "move":
				if msg.Move != nil {
					s.MoveCard(msg.Move.CardID, msg.Move.FromCol, msg.Move.ToCol, msg.Move.ToIndex)
				}
			case "textOp":
				if msg.TextOp != nil {
					s.UpdateCardText(msg.TextOp.CardID, msg.TextOp.Op, msg.TextOp.Val, msg.TextOp.Pos, msg.TextOp.Length)
				}
			case "delete":
				if msg.Delete != nil {
					s.DeleteCard(msg.Delete.CardID)
				}
			case "cursor":
				if msg.Cursor != nil {
					msg.Cursor.ID = connID
					msg.Cursor.NodeID = *nodeID
					s.SetCursor(*msg.Cursor)
				}
			}
		}
	}
}

func handleAdd(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		title := r.FormValue("title")
		if title == "" {
			title = "New Task"
		}
		store.AddCard(title)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}
