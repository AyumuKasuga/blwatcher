package main

import (
	"blwatcher/internal"
	"context"
	"fmt"
	"github.com/gorilla/feeds"
	"html/template"
	"log"
	"net/http"
	"os"
)

// Event represents a row in your events table
type Event struct {
	ID   int
	Name string
	// Add other fields as necessary
}

func main() {
	ctx := context.Background()
	connString := os.Getenv("DATABASE_URL")
	if connString == "" {
		connString = "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"
	}

	eventStorage := internal.NewEventStorage(
		ctx,
		connString,
	)

	server := http.Server{
		Addr: ":8080",
	}
	defer server.Shutdown(ctx)

	tmpl := template.Must(template.ParseFiles("templates/table.html"))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Query database
		events, err := eventStorage.GetLatestEvents(0)
		if err != nil {
			log.Printf("Error getting events: %v", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}

		// Execute template
		tmpl.Execute(w, events)
	})

	http.HandleFunc("/rss", func(w http.ResponseWriter, r *http.Request) {
		feed := &feeds.Feed{
			Title:       "Latest blacklist events of ERC20 contracts (USDT) in Ethereum network",
			Link:        &feeds.Link{Href: "https://bl.dzen.ws/rss"},
			Description: "Latest blacklist of ERC20 contracts (USDT) in Ethereum network",
		}

		events, err := eventStorage.GetLatestEvents(100)

		// Assuming 'events' is populated with your data
		for _, e := range events {
			feed.Items = append(feed.Items, &feeds.Item{
				Title:       fmt.Sprintf("%s %s %s", e.Type, e.Address, e.Date),
				Link:        &feeds.Link{Href: fmt.Sprintf("https://etherscan.io/tx/%s", e.Tx)},
				Description: fmt.Sprintf("%s %s %s", e.Type, e.Address, e.Date),
				Created:     e.Date,
			})
		}

		rss, err := feed.ToRss()
		if err != nil {
			http.Error(w, "Failed to generate RSS feed", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(rss))
	})

	log.Printf("Starting server on %s", server.Addr)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal("Server failed to start:", err)
	}
}
