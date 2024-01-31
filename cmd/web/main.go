package main

import (
	"blwatcher"
	"blwatcher/internal"
	"context"
	"fmt"
	"github.com/gorilla/feeds"
	"html/template"
	"log"
	"net/http"
	"os"
)

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

	table_tmpl := template.Must(template.ParseFiles("templates/table.html"))
	address_tmpl := template.Must(template.ParseFiles("templates/address.html"))

	type addressTmplData = struct {
		Address string
		Events  []*blwatcher.Event
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		events, err := eventStorage.GetLatestEvents(0)
		if err != nil {
			log.Printf("Error getting events: %v", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		table_tmpl.Execute(w, events)
	})

	http.HandleFunc("/address/", func(w http.ResponseWriter, r *http.Request) {
		address := r.URL.Path[len("/address/") : 9+42]
		if len(address) != 42 {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		events, err := eventStorage.GetEventsByAddress(address)
		if err != nil {
			log.Printf("Error getting events: %v", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}

		if len(events) == 0 {
			http.Error(w, http.StatusText(http.StatusNotFound), http.StatusNotFound)
			return
		}

		addressTmplData := addressTmplData{
			Address: address,
			Events:  events,
		}
		address_tmpl.Execute(w, addressTmplData)
	})

	http.HandleFunc("/rss", func(w http.ResponseWriter, r *http.Request) {
		feed := &feeds.Feed{
			Title:       "Latest blacklist events of ERC20 contracts (USDT,USDC) in Ethereum network",
			Link:        &feeds.Link{Href: "https://bl.dzen.ws/rss", Rel: "self"},
			Description: "Latest blacklist events of ERC20 contracts (USDT,USDC) in Ethereum network",
		}

		events, err := eventStorage.GetLatestEvents(100)

		for _, e := range events {
			feed.Items = append(feed.Items, &feeds.Item{
				Title:       fmt.Sprintf("%s %s %s %s", e.Type, e.Address, e.Contract.Symbol, e.Date),
				Link:        &feeds.Link{Href: fmt.Sprintf("https://bl.dzen.ws/address/%s", e.Address)},
				Description: fmt.Sprintf("%s %s %s %s", e.Type, e.Address, e.Contract.Symbol, e.Date),
				Created:     e.Date,
				Id:          fmt.Sprintf("https://bl.dzen.ws/address/%s#%s", e.Address, e.Tx),
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
