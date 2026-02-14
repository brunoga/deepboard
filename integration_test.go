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

func TestIntegration_UI(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION") != "true" {
		t.Skip("Skipping integration test. Set RUN_INTEGRATION=true and ensure playwright browsers are installed.")
	}

	// 1. Setup Server
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "integration.db")
	store, err := NewStore(dbPath, "node-test", nil)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

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
		case "/":
			handleIndex(store)(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	// 2. Setup Playwright
	pw, err := playwright.Run()
	if err != nil {
		t.Fatalf("could not start playwright: %v", err)
	}
	defer pw.Stop()

	browser, err := pw.Chromium.Launch()
	if err != nil {
		t.Fatalf("could not launch browser: %v", err)
	}
	defer browser.Close()

	// 3. Simulating User 1
	context1, _ := browser.NewContext()
	page1, _ := context1.NewPage()
	if _, err := page1.Goto(server.URL); err != nil {
		t.Fatalf("could not goto server: %v", err)
	}

	// 4. Simulating User 2
	context2, _ := browser.NewContext()
	page2, _ := context2.NewPage()
	if _, err := page2.Goto(server.URL); err != nil {
		t.Fatalf("could not goto server: %v", err)
	}

	// Wait for connections to establish and stats to update
	time.Sleep(2 * time.Second)

	// --- TEST CASE: Connection Counts ---
	t.Run("ConnectionCountsUpdate", func(t *testing.T) {
		stats, _ := page1.TextContent("#conn-counts")
		if !strings.Contains(stats, "Total: 2") {
			t.Errorf("Expected Total: 2 in stats, got %s", stats)
		}
	})

	// --- TEST CASE: Add Card Sync ---
	t.Run("AddCardSync", func(t *testing.T) {
		cardTitle := "Integration Task " + fmt.Sprint(time.Now().Unix())
		
		// User 1 adds a card
		page1.Fill("input[name='title']", cardTitle)
		page1.Click("button:has-text('Add Task')")

		// User 2 should see it
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
			t.Error("User 2 did not see the new card after sync")
		}
	})

	// --- TEST CASE: Text Synchronization ---
	t.Run("TextSync", func(t *testing.T) {
		// User 1 types in the first description they find
		description := "UI synchronized text"
		
		// Select the first textarea on page 1
		textareas1, _ := page1.QuerySelectorAll(".card-desc")
		if len(textareas1) == 0 {
			t.Fatal("No cards found for text sync test")
		}
		
		textareas1[0].Type(description)
		textareas1[0].Press("Enter")
		
		// Trigger the 'input' event which is debounced/throttled
		time.Sleep(2 * time.Second)

		// User 2 should see the update
		found := false
		for i := 0; i < 20; i++ {
			textareas2, _ := page2.QuerySelectorAll(".card-desc")
			if len(textareas2) > 0 {
				val, _ := textareas2[0].InputValue()
				if strings.Contains(val, description) {
					found = true
					break
				}
			}
			time.Sleep(500 * time.Millisecond)
		}

		if !found {
			t.Error("User 2 did not see the text description update")
		}
	})
}
