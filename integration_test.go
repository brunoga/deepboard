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
	server  *httptest.Server
	store   *Store
	pw      *playwright.Playwright
	browser playwright.Browser
}

func setupIntegration(t *testing.T) *testEnv {
	if os.Getenv("RUN_INTEGRATION") != "true" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION=true.")
	}

	// 1. Fresh Database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "integration.db")
	store, err := NewStore(dbPath, "node-test", nil)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	// 2. Fresh Server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws":
			handleWS(store)(w, r)
		case "/board":
			handleBoard(store)(w, r)
		case "/stats":
			handleStats(store)(w, r)
		case "/history":
			handleHistory(store)(w, r)
		case "/api/add":
			handleAdd(store)(w, r)
		case "/api/history/clear":
			handleClearHistory(store)(w, r)
		case "/api/admin/reset":
			handleReset(store)(w, r)
		case "/":
			handleIndex(store)(w, r)
		default:
			http.NotFound(w, r)
		}
	}))

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
		server:  server,
		store:   store,
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
	page1.Goto(env.server.URL)

	ctx2, _ := env.browser.NewContext()
	page2, _ := ctx2.NewPage()
	page2.On("console", func(msg playwright.ConsoleMessage) {
		fmt.Printf("PAGE2: %s\n", msg.Text())
	})
	page2.Goto(env.server.URL)

	return page1, page2
}

func (e *testEnv) teardown() {
	e.browser.Close()
	e.pw.Stop()
	e.server.Close()
}

func TestIntegration_ConnectionCounts(t *testing.T) {
	env := setupIntegration(t)
	defer env.teardown()

	page1, _ := setupPages(t, env)

	// Wait for WS to establish
	time.Sleep(1 * time.Second)

	stats, _ := page1.TextContent("#conn-counts")
	if !strings.Contains(stats, "Total: 2") {
		t.Errorf("Expected Total: 2, got %s", stats)
	}
}

func TestIntegration_AddCard(t *testing.T) {
	env := setupIntegration(t)
	defer env.teardown()

	page1, page2 := setupPages(t, env)

	cardTitle := "Sync Task"
	page1.Fill("input[name='title']", cardTitle)
	page1.Click("button:has-text('Add Task')")

	// Verify User 2 sees it
	found := false
	for i := 0; i < 10; i++ {
		content, _ := page2.Content()
		if strings.Contains(content, cardTitle) {
			found = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !found {
		t.Error("User 2 did not see the new card")
	}
}

func TestIntegration_TextSync(t *testing.T) {
	env := setupIntegration(t)
	defer env.teardown()

	page1, page2 := setupPages(t, env)

	// Use initial card
	description := "Live sync content"
	textareas1, _ := page1.QuerySelectorAll(".card-desc")
	textareas1[0].Type(description)
	textareas1[0].Press("Enter")

	time.Sleep(2 * time.Second)

	found := false
	for i := 0; i < 10; i++ {
		textareas2, _ := page2.QuerySelectorAll(".card-desc")
		val, _ := textareas2[0].InputValue()
		if strings.Contains(val, description) {
			found = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !found {
		t.Error("Text did not synchronize")
	}
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
	time.Sleep(2 * time.Second)

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
	locator := page1.Locator(".card", playwright.PageLocatorOptions{
		Has: page1.Locator("text=" + title),
	})
	
	// Wait for the card to be hidden (removed from DOM)
	err := locator.WaitFor(playwright.LocatorWaitForOptions{
		State: playwright.WaitForSelectorStateHidden,
		Timeout: playwright.Float(5000),
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
	time.Sleep(1 * time.Second)

	// Admin Reset
	page1.On("dialog", func(dialog playwright.Dialog) {
		dialog.Accept()
	})
	page1.Click("button:has-text('Reset Board')")

	// User 2 should be back to initial state (1 sample card)
	time.Sleep(2 * time.Second)
	cards, _ := page2.QuerySelectorAll(".card")
	if len(cards) != 1 {
		t.Errorf("Expected 1 card after reset, got %d", len(cards))
	}
}

func TestIntegration_MoveCard(t *testing.T) {
	env := setupIntegration(t)
	defer env.teardown()

	page1, page2 := setupPages(t, env)

	// Drag card-1 from TODO to IN PROGRESS
	inProgress := "#col-in-progress"

	card := page1.Locator(".card >> text=Try Deep Library")
	card.DragTo(page1.Locator(inProgress))

	time.Sleep(2 * time.Second)

	// Verify User 2 sees it in In Progress
	found := false
	for i := 0; i < 10; i++ {
		// Check if card exists inside in-progress column
		count, _ := page2.Locator("#col-in-progress .card >> text=Try Deep Library").Count()
		if count > 0 {
			found = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !found {
		t.Error("Card move did not synchronize")
	}
}
