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

- Go 1.24 or later

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
Since this is a sample toy project:
1. **Missed Updates:** If a node is offline when a Delta is broadcast, it will currently miss that update.
2. **Eventual Consistency:** In a full implementation, nodes would perform a "Sync Handshake" on startup, exchanging missing patches to catch up to the latest state. The `deep` library supports this, but it is not implemented in this basic demo.
3. **Conflict Resolution:** Even with missed updates, the CRDT ensures that once nodes *do* receive the same set of updates (even out of order), they will converge to the exact same state.

## License

This project is licensed under the Apache License, Version 2.0. See the [LICENSE](LICENSE) file for details.
