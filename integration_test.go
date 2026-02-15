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

	// 4. Verify convergence (Relaxed check)
	// We check if both converged to THE SAME value, and if that value contains both edits.
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
