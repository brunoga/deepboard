package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"
)

type testEnv struct {
	server1 *httptest.Server
	server2 *httptest.Server
	store1  *Store
	store2  *Store
	pw      *playwright.Playwright
	browser playwright.Browser
}

func setupIntegration(t *testing.T) *testEnv {
	if os.Getenv("RUN_INTEGRATION") != "true" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION=true.")
	}

	// 1. Fresh Databases
	tmpDir := t.TempDir()
	dbPath1 := filepath.Join(tmpDir, "integration1.db")
	dbPath2 := filepath.Join(tmpDir, "integration2.db")

	store1, err := NewStore(dbPath1, "node-1", nil)
	if err != nil {
		t.Fatalf("failed to create store 1: %v", err)
	}
	store2, err := NewStore(dbPath2, "node-2", nil)
	if err != nil {
		t.Fatalf("failed to create store 2: %v", err)
	}

	// 2. Fresh Servers
	handler1 := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws":
			handleWS(store1)(w, r)
		case "/board":
			handleBoard(store1)(w, r)
		case "/stats":
			handleStats(store1)(w, r)
		case "/history":
			handleHistory(store1)(w, r)
		case "/api/add":
			handleAdd(store1)(w, r)
		case "/api/sync":
			handleSync(store1)(w, r)
		case "/api/state":
			handleState(store1)(w, r)
		case "/api/history/clear":
			handleClearHistory(store1)(w, r)
		case "/api/admin/reset":
			handleReset(store1)(w, r)
		case "/":
			handleIndex(store1)(w, r)
		default:
			http.NotFound(w, r)
		}
	}

	handler2 := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws":
			handleWS(store2)(w, r)
		case "/board":
			handleBoard(store2)(w, r)
		case "/stats":
			handleStats(store2)(w, r)
		case "/history":
			handleHistory(store2)(w, r)
		case "/api/add":
			handleAdd(store2)(w, r)
		case "/api/sync":
			handleSync(store2)(w, r)
		case "/api/state":
			handleState(store2)(w, r)
		case "/api/history/clear":
			handleClearHistory(store2)(w, r)
		case "/api/admin/reset":
			handleReset(store2)(w, r)
		case "/":
			handleIndex(store2)(w, r)
		default:
			http.NotFound(w, r)
		}
	}

	server1 := httptest.NewServer(http.HandlerFunc(handler1))
	server2 := httptest.NewServer(http.HandlerFunc(handler2))

	// Link them as peers
	store1.UpdatePeers([]string{strings.TrimPrefix(server2.URL, "http://")})
	store2.UpdatePeers([]string{strings.TrimPrefix(server1.URL, "http://")})

	// 3. Playwright
	pw, err := playwright.Run()
	if err != nil {
		t.Fatalf("could not start playwright: %v", err)
	}

	browser, err := pw.Chromium.Launch()
	if err != nil {
		t.Fatalf("could not launch browser: %v", err)
	}

	return &testEnv{
		server1: server1,
		server2: server2,
		store1:  store1,
		store2:  store2,
		pw:      pw,
		browser: browser,
	}
}

func setupPages(t *testing.T, env *testEnv) (playwright.Page, playwright.Page) {
	ctx1, _ := env.browser.NewContext()
	page1, _ := ctx1.NewPage()
	page1.On("console", func(msg playwright.ConsoleMessage) {
		fmt.Printf("PAGE1: %s\n", msg.Text())
	})
	page1.Goto(env.server1.URL)

	ctx2, _ := env.browser.NewContext()
	page2, _ := ctx2.NewPage()
	page2.On("console", func(msg playwright.ConsoleMessage) {
		fmt.Printf("PAGE2: %s\n", msg.Text())
	})
	page2.Goto(env.server2.URL)

	return page1, page2
}

func waitForCondition(t *testing.T, desc string, condition func() bool) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Errorf("Timed out waiting for: %s", desc)
}

func (e *testEnv) teardown() {
	e.browser.Close()
	e.pw.Stop()
	e.server1.Close()
	e.server2.Close()
}

func TestIntegration_ConnectionCounts(t *testing.T) {
	env := setupIntegration(t)
	defer env.teardown()

	page1, _ := setupPages(t, env)

	waitForCondition(t, "connection count to reach 2", func() bool {
		stats, _ := page1.TextContent("#conn-counts")
		return strings.Contains(stats, "Total: 2")
	})
}

func TestIntegration_ConnectionCountDecrease(t *testing.T) {
	env := setupIntegration(t)
	defer env.teardown()

	page1, page2 := setupPages(t, env)

	// 1. Wait for both to connect
	waitForCondition(t, "connection count to reach 2", func() bool {
		stats, _ := page1.TextContent("#conn-counts")
		return strings.Contains(stats, "Total: 2")
	})

	// 2. Close page 2
	err := page2.Close()
	if err != nil {
		t.Fatalf("Failed to close page 2: %v", err)
	}

	// 3. Verify total count on page 1 decreases to 1
	// Note: It should happen quickly because Unsubscribe is immediate.
	waitForCondition(t, "connection count to decrease to 1", func() bool {
		stats, _ := page1.TextContent("#conn-counts")
		return strings.Contains(stats, "Total: 1")
	})
}

func TestIntegration_AddCard(t *testing.T) {
	env := setupIntegration(t)
	defer env.teardown()

	page1, page2 := setupPages(t, env)

	cardTitle := "Sync Task"
	page1.Fill("input[name='title']", cardTitle)
	page1.Click("button:has-text('Add Task')")

	waitForCondition(t, "User 2 to see new card", func() bool {
		content, _ := page2.Content()
		return strings.Contains(content, cardTitle)
	})
}

func TestIntegration_TextSync(t *testing.T) {
	env := setupIntegration(t)
	defer env.teardown()

	page1, page2 := setupPages(t, env)

	// Use initial card
	description := "Live sync content"
	ta := page1.Locator(".card-desc").First()
	ta.Type(description)
	ta.Blur()

	waitForCondition(t, "User 2 to see text", func() bool {
		textareas2, _ := page2.QuerySelectorAll(".card-desc")
		if len(textareas2) == 0 {
			return false
		}
		val, _ := textareas2[0].InputValue()
		return strings.Contains(val, description)
	})
}

func TestIntegration_DeleteCard(t *testing.T) {
	env := setupIntegration(t)
	defer env.teardown()

	page1, page2 := setupPages(t, env)

	// Add a card first
	title := "To be deleted"
	page1.Fill("input[name='title']", title)
	page1.Click("button:has-text('Add Task')")

	// Wait for it to appear on both
	locator1 := page1.Locator(".card", playwright.PageLocatorOptions{
		Has: page1.Locator("text=" + title),
	})
	locator2 := page2.Locator(".card", playwright.PageLocatorOptions{
		Has: page2.Locator("text=" + title),
	})
	
	// Wait for appearance first
	err := locator1.WaitFor()
	if err != nil { t.Fatal("Card never appeared on page 1") }
	err = locator2.WaitFor()
	if err != nil { t.Fatal("Card never appeared on page 2") }

	// User 2 deletes it
	page2.On("dialog", func(dialog playwright.Dialog) {
		dialog.Accept()
	})
	// Target the specific card's delete button
	cardToDelete := page2.Locator(".card", playwright.PageLocatorOptions{
		Has: page2.Locator("text=" + title),
	})
	cardToDelete.Locator(".delete-btn").Click()

	// Verify User 1 sees it's gone
	// Wait for the card to be hidden (removed from DOM)
	err = locator1.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateHidden,
		Timeout: playwright.Float(10000),
	})
	
	if err != nil {
		t.Errorf("Card was not deleted for other users (still visible in DOM)")
	}
}

func TestIntegration_ResetBoard(t *testing.T) {
	env := setupIntegration(t)
	defer env.teardown()

	page1, page2 := setupPages(t, env)

	// Add many cards
	for i := 0; i < 3; i++ {
		page1.Fill("input[name='title']", fmt.Sprintf("Task %d", i))
		page1.Click("button:has-text('Add Task')")
	}
	// No explicit sleep, just wait for Reset button to be interactable?
	// It is always there.

	// Admin Reset
	page1.On("dialog", func(dialog playwright.Dialog) {
		dialog.Accept()
	})
	page1.Click("button:has-text('Reset Board')")

	// User 2 should be back to initial state (1 sample card)
	waitForCondition(t, "User 2 to have 1 card", func() bool {
		cards, _ := page2.QuerySelectorAll(".card")
		return len(cards) == 1
	})
}

func TestIntegration_MoveCard(t *testing.T) {
	env := setupIntegration(t)
	defer env.teardown()

	page1, page2 := setupPages(t, env)

	// Drag card-1 from TODO to IN PROGRESS
	inProgress := "#col-in-progress"

	card := page1.Locator(".card >> text=Try Deep Library")
	card.DragTo(page1.Locator(inProgress))

	// Verify User 2 sees it in In Progress
	waitForCondition(t, "User 2 to see moved card", func() bool {
		count, _ := page2.Locator("#col-in-progress .card >> text=Try Deep Library").Count()
		return count > 0
	})
}

func TestIntegration_ConcurrentMoveAndEdit(t *testing.T) {
	env := setupIntegration(t)
	defer env.teardown()

	page1, page2 := setupPages(t, env)

	// Target the initial card
	inProgress := "#col-in-progress"
	card1 := page1.Locator(".card >> text=Try Deep Library")
	textarea2 := page2.Locator(".card-desc").First()

	// 1. User 1 moves card
	card1.DragTo(page1.Locator(inProgress))

	// 2. User 2 immediately edits the same card
	edit := " (Edited Simultaneously)"
	textarea2.Focus()
	textarea2.Type(edit)
	textarea2.Blur()

	// 3. Verify both see it in the new column with the edit
	waitForCondition(t, "Both users see moved and edited card", func() bool {
		for _, p := range []playwright.Page{page1, page2} {
			count, _ := p.Locator("#col-in-progress .card >> text=Try Deep Library").Count()
			if count == 0 {
				return false
			}
			val, _ := p.Locator(".card-desc").First().InputValue()
			if !strings.Contains(val, edit) {
				return false
			}
		}
		return true
	})
}

// TestIntegration_NoDuplicateOnConcurrentRefresh reproduces the duplicate-character
// bug: when refreshUI() (triggered by a peer edit) calls initTextareas() while a
// debounce timer is still pending, the old closure's timer fires with a stale
// baseline and sends an overlapping insert alongside the new closure's timer.
// The fix is to register the handler only once per element and store the timeout
// on the element so clearTimeout always cancels the right timer.
func TestIntegration_NoDuplicateOnConcurrentRefresh(t *testing.T) {
	env := setupIntegration(t)
	defer env.teardown()

	page1, page2 := setupPages(t, env)

	// Wait for both nodes to be connected.
	waitForCondition(t, "connection count to reach 2", func() bool {
		stats, _ := page1.TextContent("#conn-counts")
		return strings.Contains(stats, "Total: 2")
	})

	// Add a fresh card so its description textarea starts empty.
	page1.Locator("input[name='title']").Fill("Typing Test Card")
	page1.Locator("button:has-text('Add Task')").Click()

	newCard1 := page1.Locator(".card", playwright.PageLocatorOptions{Has: page1.Locator("text=Typing Test Card")})
	newCard2 := page2.Locator(".card", playwright.PageLocatorOptions{Has: page2.Locator("text=Typing Test Card")})
	if err := newCard1.WaitFor(); err != nil {
		t.Fatal("new card never appeared on page 1")
	}
	if err := newCard2.WaitFor(); err != nil {
		t.Fatal("new card never appeared on page 2")
	}

	ta1 := newCard1.Locator(".card-desc")
	ta2 := newCard2.Locator(".card-desc")
	ta1.Click()

	// Type each character at ~100 ms intervals. After each keypress, immediately
	// call initTextareas() — exactly what refreshUI() does when a peer message
	// arrives. This reproduces the dangling-timer race without depending on
	// network timing.
	typed := "hello"
	for _, ch := range typed {
		page1.Keyboard().Type(string(ch))
		time.Sleep(100 * time.Millisecond)
		page1.Evaluate("() => { if (typeof initTextareas === 'function') initTextareas(); }", nil)
	}

	// Let the last debounce settle and flush to the server.
	time.Sleep(400 * time.Millisecond)

	val1, _ := ta1.InputValue()
	if val1 != typed {
		t.Errorf("page 1 textarea = %q, want %q (duplicate chars?)", val1, typed)
	}

	// Wait for the edit to propagate to node 2.
	waitForCondition(t, "page 2 to see correct text", func() bool {
		val, _ := ta2.InputValue()
		return val == typed
	})

	val2, _ := ta2.InputValue()
	if val2 != typed {
		t.Errorf("page 2 textarea = %q, want %q", val2, typed)
	}
}

// TestIntegration_RemoteDeletePropagates is the regression test for the bug
// where a character deletion on node 2 was not reflected on node 1 because the
// 1-second lastInputTime window in refreshUI blocked the incoming update.
//
// With the fix (_pendingOp flag), the update is only blocked during the narrow
// 250 ms debounce window; once the debounce has fired the refresh applies
// immediately regardless of how recently the element was edited.
func TestIntegration_RemoteDeletePropagates(t *testing.T) {
	env := setupIntegration(t)
	defer env.teardown()

	page1, page2 := setupPages(t, env)

	waitForCondition(t, "both nodes connected", func() bool {
		stats, _ := page1.TextContent("#conn-counts")
		return strings.Contains(stats, "Total: 2")
	})

	page1.Locator("input[name='title']").Fill("Delete Propagation Test")
	page1.Locator("button:has-text('Add Task')").Click()

	card1 := page1.Locator(".card", playwright.PageLocatorOptions{Has: page1.Locator("text=Delete Propagation Test")})
	card2 := page2.Locator(".card", playwright.PageLocatorOptions{Has: page2.Locator("text=Delete Propagation Test")})
	if err := card1.WaitFor(); err != nil {
		t.Fatal("card never appeared on page 1")
	}
	if err := card2.WaitFor(); err != nil {
		t.Fatal("card never appeared on page 2")
	}

	ta1 := card1.Locator(".card-desc")
	ta2 := card2.Locator(".card-desc")

	// Node 1 types "1234567890" and blurs so the debounce fires and syncs.
	ta1.Click()
	ta1.Type("1234567890")
	ta1.Blur()

	waitForCondition(t, "node 2 to receive full string", func() bool {
		v, _ := ta2.InputValue()
		return v == "1234567890"
	})

	// Node 2 selects the '7' (index 6) and deletes it.
	ta2.Evaluate("el => { el.focus(); el.setSelectionRange(6, 7); }", nil)
	page2.Keyboard().Press("Backspace")
	ta2.Blur()

	// Node 1 — which is idle (not focused, no pending op) — must reflect the
	// deletion without waiting for a manual catch-up timeout.
	waitForCondition(t, "node 1 to see deletion from node 2", func() bool {
		v, _ := ta1.InputValue()
		return v == "123456890"
	})
	if v, _ := ta1.InputValue(); v != "123456890" {
		t.Errorf("node 1 textarea = %q, want %q", v, "123456890")
	}
}

// TestIntegration_PendingOpNotClobberedByRefresh verifies that the _pendingOp
// flag protects a textarea whose debounce has not yet fired from being
// overwritten by a concurrent refreshUI call.
//
// Scenario:
//  1. Node 1 types "A" and immediately blurs (activeId = nil), leaving _pendingOp
//     true and the debounce timer running.
//  2. A refreshUI() is forced before the debounce fires. At this point the server
//     still has an empty description, so a naive refresh would reset el.value to "".
//  3. _pendingOp must block that overwrite.
//  4. The debounce fires, sends the insert, and both nodes converge on "A".
func TestIntegration_PendingOpNotClobberedByRefresh(t *testing.T) {
	env := setupIntegration(t)
	defer env.teardown()

	page1, page2 := setupPages(t, env)

	waitForCondition(t, "both nodes connected", func() bool {
		stats, _ := page1.TextContent("#conn-counts")
		return strings.Contains(stats, "Total: 2")
	})

	page1.Locator("input[name='title']").Fill("Pending Op Test")
	page1.Locator("button:has-text('Add Task')").Click()

	card1 := page1.Locator(".card", playwright.PageLocatorOptions{Has: page1.Locator("text=Pending Op Test")})
	card2 := page2.Locator(".card", playwright.PageLocatorOptions{Has: page2.Locator("text=Pending Op Test")})
	if err := card1.WaitFor(); err != nil {
		t.Fatal("card never appeared on page 1")
	}
	if err := card2.WaitFor(); err != nil {
		t.Fatal("card never appeared on page 2")
	}

	ta1 := card1.Locator(".card-desc")
	ta2 := card2.Locator(".card-desc")

	// Focus, type "A", then blur — _pendingOp is now true, debounce running.
	ta1.Click()
	page1.Keyboard().Type("A")
	ta1.Blur() // activeId = null; only _pendingOp protects the element

	// Force a refresh before the 250 ms debounce fires. The server still has an
	// empty description, so refreshUI would set el.value = "" without _pendingOp.
	page1.Evaluate("() => refreshUI()", nil)

	// Let the debounce settle and the op propagate.
	time.Sleep(400 * time.Millisecond)

	// Node 1 must still show "A" — not "" from the premature refresh.
	if v, _ := ta1.InputValue(); v != "A" {
		t.Errorf("node 1 textarea was clobbered by refresh: got %q, want \"A\"", v)
	}

	// Node 2 must receive the insert from node 1.
	waitForCondition(t, "node 2 to see 'A' from node 1", func() bool {
		v, _ := ta2.InputValue()
		return v == "A"
	})
	if v, _ := ta2.InputValue(); v != "A" {
		t.Errorf("node 2 textarea = %q, want \"A\"", v)
	}
}

func TestIntegration_ConcurrentConflictingEdits(t *testing.T) {
	env := setupIntegration(t)
	defer env.teardown()

	page1, page2 := setupPages(t, env)

	// 1. Setup initial state
	cardTitle := "CRDT Conflict Test"
	page1.Fill("input[name='title']", cardTitle)
	page1.Click("button:has-text('Add Task')")

	// Wait for card to appear on both
	locator1 := page1.Locator(".card", playwright.PageLocatorOptions{Has: page1.Locator("text=" + cardTitle)})
	locator2 := page2.Locator(".card", playwright.PageLocatorOptions{Has: page2.Locator("text=" + cardTitle)})
	locator1.WaitFor()
	locator2.WaitFor()

	ta1 := locator1.Locator(".card-desc")
	ta2 := locator2.Locator(".card-desc")

	// 2. Set base text
	initialText := "Test CRDT conflicting edits"
	ta1.Fill(initialText)
	ta1.Blur()

	// Wait for base text to sync
	waitForCondition(t, "Base text sync", func() bool {
		v, _ := ta2.InputValue()
		return v == initialText
	})

	// 3. Perform concurrent edits
	// User 1: Insert "USER1 " between "Test " and "CRDT"
	// User 2: Insert "USER2 " between "conflicting " and "edits"
	
	// We use Evaluate to set cursor position precisely
	ta1.Evaluate("el => { el.focus(); el.setSelectionRange(5, 5); }", nil)
	page1.Keyboard().Type("USER1 ")
	
	// Small sleep to ensure events are processed
	time.Sleep(100 * time.Millisecond)

	ta2.Evaluate("el => { el.focus(); el.setSelectionRange(22, 22); }", nil)
	page2.Keyboard().Type("USER2 ")

	// Trigger sync
	ta1.Blur()
	ta2.Blur()

	// 4. Verify convergence: both nodes agree and both edits are preserved.
	waitForCondition(t, "Convergence", func() bool {
		lastVal1, _ := ta1.InputValue()
		lastVal2, _ := ta2.InputValue()

		if lastVal1 != lastVal2 {
			return false
		}

		if !strings.Contains(lastVal1, "USER1") || !strings.Contains(lastVal1, "USER2") {
			return false
		}

		return true
	})
}

func TestIntegration_TextDeleteSync(t *testing.T) {
	env := setupIntegration(t)
	defer env.teardown()

	page1, page2 := setupPages(t, env)

	waitForCondition(t, "both nodes connected", func() bool {
		stats, _ := page1.TextContent("#conn-counts")
		return strings.Contains(stats, "Total: 2")
	})

	// 1. Node 1 adds a card
	title := "Delete Sync Test"
	page1.Locator("input[name='title']").Fill(title)
	page1.Locator("button:has-text('Add Task')").Click()

	card1 := page1.Locator(".card", playwright.PageLocatorOptions{Has: page1.Locator("text=" + title)})
	card2 := page2.Locator(".card", playwright.PageLocatorOptions{Has: page2.Locator("text=" + title)})
	card1.WaitFor()
	card2.WaitFor()

	ta1 := card1.Locator(".card-desc")
	ta2 := card2.Locator(".card-desc")

	// 2. Node 1 types "ABCDEF"
	ta1.Click()
	page1.Keyboard().Type("ABCDEF")
	ta1.Blur()
	waitForCondition(t, "Node 2 sees ABCDEF", func() bool {
		v, _ := ta2.InputValue()
		return v == "ABCDEF"
	})

	// 2. Node 2 deletes "DEF"
	ta2.Focus()
	page2.Keyboard().Press("End")
	for i := 0; i < 3; i++ { page2.Keyboard().Press("Backspace") }
	ta2.Blur()

	// 3. Verify Node 1 sees "ABC"
	waitForCondition(t, "Node 1 sees ABC", func() bool {
		v, _ := ta1.InputValue()
		return v == "ABC"
	})

	// 4. Node 1 types "GHI"
	ta1.Focus()
	page1.Keyboard().Press("End")
	page1.Keyboard().Type("GHI")
	ta1.Blur()

	// 5. Verify Node 2 sees "ABCGHI"
	waitForCondition(t, "Node 2 sees ABCGHI", func() bool {
		v, _ := ta2.InputValue()
		return v == "ABCGHI"
	})
}

// TestIntegration_ReproSyncIssue attempts to reproduce the reported bug:
// 1. Node 1 adds a card and types "Hello World".
// 2. Node 2 sees "Hello World".
// 3. Node 2 deletes "World" while Node 1 keeps the textarea FOCUSED.
// 4. Node 1 types "!!" at the end.
// 5. Verify Node 2 eventually sees "Hello !!" (or whatever is correct for the CRDT).
func TestIntegration_ReproSyncIssue(t *testing.T) {
	env := setupIntegration(t)
	defer env.teardown()

	page1, page2 := setupPages(t, env)

	waitForCondition(t, "both nodes connected", func() bool {
		stats, _ := page1.TextContent("#conn-counts")
		return strings.Contains(stats, "Total: 2")
	})

	// 1. Node 1 adds a card
	page1.Locator("input[name='title']").Fill("Repro Card")
	page1.Locator("button:has-text('Add Task')").Click()

	card1 := page1.Locator(".card", playwright.PageLocatorOptions{Has: page1.Locator("text=Repro Card")})
	card2 := page2.Locator(".card", playwright.PageLocatorOptions{Has: page2.Locator("text=Repro Card")})
	card1.WaitFor()
	card2.WaitFor()

	ta1 := card1.Locator(".card-desc")
	ta2 := card2.Locator(".card-desc")

	// 2. Node 1 types "Hello World" and BLURS to ensure base sync
	ta1.Click()
	page1.Keyboard().Type("Hello World")
	ta1.Blur()

	waitForCondition(t, "Node 2 sees Hello World", func() bool {
		v, _ := ta2.InputValue()
		return v == "Hello World"
	})

	// 3. Node 1 FOCUSES again (to trigger the 'skip refresh' logic)
	ta1.Focus()
	page1.Keyboard().Press("End") // Move cursor to end
	time.Sleep(100 * time.Millisecond)

	// 4. Node 2 deletes "World"
	ta2.Focus()
	page2.Keyboard().Press("End")
	for i := 0; i < 5; i++ { page2.Keyboard().Press("Backspace") }
	ta2.Blur()

	// Wait a bit to ensure Node 1 received the refresh message but skipped UI update due to focus
	time.Sleep(1000 * time.Millisecond)

	// 5. Node 1 types "!!" at the end (without having blurred since the remote delete)
	page1.Keyboard().Type("!!")
	// ta1.Blur() // Don't blur yet, let it debounce naturally

	// 6. Verify Node 2 receives the update
	waitForCondition(t, "Node 2 receives update after Node 1 types", func() bool {
		v, _ := ta2.InputValue()
		return strings.Contains(v, "!!")
	})

	v2, _ := ta2.InputValue()
	if !strings.Contains(v2, "Hello") || !strings.Contains(v2, "!!") {
		t.Errorf("Node 2 state is corrupted: %q", v2)
	}
}

