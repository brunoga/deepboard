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

To simulate multiple nodes, run the application with different ports and database paths:

```bash
go run . -addr :8081 -db node2.db
```

## License

This project is licensed under the Apache License, Version 2.0. See the [LICENSE](LICENSE) file for details.
