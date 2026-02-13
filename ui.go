package main

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

        .reset-btn { background: #ff0000; color: white; border: 4px solid #8b0000; border-radius: 8px; padding: 10px 20px; font-size: 1.2rem; font-weight: bold; cursor: pointer; text-transform: uppercase; box-shadow: 0 4px 0 #8b0000; transition: all 0.1s; margin-left: 20px; }
        .reset-btn:active { transform: translateY(2px); box-shadow: 0 2px 0 #8b0000; }
        .reset-btn:hover { background: #ff3333; }
    </style>
</head>
<body>
    <header>
        <h1>DeepBoard <span style="font-size: 0.8rem; color: #3498db; vertical-align: middle;">(Node: {{.NodeID}})</span></h1>
        <div id="connection-stats" style="color: #bdc3c7; font-size: 0.8rem; margin-left: auto; margin-right: 20px;">
            Local: {{.LocalCount}} | Total: {{.TotalCount}}
            <span onclick="cleanupConnections()" style="cursor: pointer; margin-left: 10px; text-decoration: underline;" title="Force cleanup of stale nodes">🧹</span>
        </div>
        <button onclick="resetBoard()" class="reset-btn">Reset Board</button>
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
                const msg = JSON.parse(e.data);
                if (msg.type === 'refresh' && !msg.silent) refreshUI();
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
                
                // If nothing is being edited, just replace the whole board for 100% reliability
                if (!activeId) {
                    document.getElementById('board').innerHTML = html;
                    initSortable(); initTextareas();
                    return;
                }

                const cardLists = temp.querySelectorAll('.card-list');
                if (cardLists.length === 0) {
                    console.error('No card lists found in /board response');
                    return;
                }

                cardLists.forEach(newList => {
                    const oldList = document.getElementById(newList.id);
                    if (!oldList) return;

                    const newCards = Array.from(newList.querySelectorAll('.card'));
                    const newIds = new Set(newCards.map(c => c.dataset.id));

                    // 1. Remove cards that are no longer present
                    oldList.querySelectorAll('.card').forEach(oldCard => {
                        if (!newIds.has(oldCard.dataset.id)) oldCard.remove();
                    });

                    // 2. Update existing or add new
                    newCards.forEach(newCard => {
                        const oldCard = oldList.querySelector('[data-id="' + newCard.dataset.id + '"]');
                        if (!oldCard) {
                            oldList.appendChild(newCard.cloneNode(true));
                        } else {
                            // Update title and presence
                            oldCard.querySelector('.card-title').innerText = newCard.querySelector('.card-title').innerText;
                            oldCard.querySelector('.presence-list').innerHTML = newCard.querySelector('.presence-list').innerHTML;
                            
                            const oldTA = oldCard.querySelector('.card-desc');
                            const newTA = newCard.querySelector('.card-desc');
                            const now = Date.now(), lastTyped = lastInputTime[oldTA.id] || 0;
                            
                            if (oldTA.id === activeId || (now - lastTyped < 1000)) {
                                // Keep local text, but schedule a catch-up
                                clearTimeout(refreshTimeout);
                                refreshTimeout = setTimeout(refreshUI, 1100);
                            } else if (oldTA.value !== newTA.value) {
                                oldTA.value = newTA.value;
                                oldTA.dataset.lastValue = newTA.value;
                            }
                        }
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

        function resetBoard() {
            if (confirm('DANGER: This will wipe EVERYTHING and reset the board for all users. Are you absolutely sure?')) {
                fetch('/api/admin/reset').then(() => refreshUI());
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
