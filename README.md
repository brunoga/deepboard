# DeepBoard

DeepBoard is a sample toy project designed to demonstrate how the [Deep](https://github.com/brunoga/deep) library can be used to build collaborative applications. It uses CRDTs (Conflict-free Replicated Data Types) provided by the Deep library for real-time synchronization of complex state across multiple nodes.

**Note:** This is a demonstration project and is not intended for production use.

## Features

- **Real-time Collaboration:** Multiple users can edit the board simultaneously.
- **Kanban Structure:** Organize tasks into "To Do", "In Progress", and "Done" columns.
- **Persistent Storage:** Uses SQLite to persist the board state and history.
- **CRDT-powered:** Seamlessly handles concurrent updates to card titles, descriptions, and positions using the Deep library.

## Getting Started

### Prerequisites

- Go 1.25 or later

### Running DeepBoard

1. Clone the repository:
   ```bash
   git clone https://github.com/brunoga/deepboard.git
   cd deepboard
   ```

2. Run the application:
   ```bash
   go run .
   ```

3. Open your browser and navigate to `http://localhost:8080`.

### Running Multiple Nodes

To see real-time synchronization in action, you can run multiple instances and connect them using the `-peers` flag:

**Node 1:**
```bash
go run . -addr :8080 -db node1.db -peers localhost:8081
```

**Node 2:**
```bash
go run . -addr :8081 -db node2.db -peers localhost:8080
```

Any change made on one board will be pushed to the other instantly.

## How Syncing Works (and its limitations)

This project uses a simple "Push" gossip model:
- When you make a change, the node generates a **Delta**.
- It immediately tries to HTTP POST this Delta to all listed `-peers`.
- The receiving node applies the Delta to its local CRDT state.

### What if a node is offline?
1. **State Sync on Connect:** When a node starts, it immediately attempts to fetch the full CRDT state from all known `-peers` and merges it locally.
2. **Background Sync:** The node runs a background loop (every 30 seconds) that re-syncs state from peers. This ensures that even if a node was offline during a broadcast, it will eventually catch up.
3. **Conflict Resolution:** The `deep` library uses LWW (Last-Write-Wins) and state-based merging to ensure that once nodes share data, they converge to the exact same state regardless of update order.

## License

This project is licensed under the Apache License, Version 2.0. See the [LICENSE](LICENSE) file for details.
