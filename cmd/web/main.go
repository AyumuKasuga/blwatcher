package main

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/gorilla/feeds"

	"blwatcher"
	"blwatcher/internal"
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
	defer func() {
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("Server shutdown error: %v", err)
		}
	}()

	table_tmpl := template.Must(template.ParseFiles("templates/table.html"))
	address_tmpl := template.Must(template.ParseFiles("templates/address.html"))

	type addressTmplData = struct {
		Address string
		Events  []*blwatcher.Event
	}

	type indexTmplData = struct {
		Events []*blwatcher.Event
		Short  bool
		Filter string
	}

	parseFilter := func(value string) *blwatcher.Blockchain {
		switch strings.ToLower(value) {
		case "tron":
			b := blwatcher.BlockchainTron
			return &b
		case "ethereum":
			b := blwatcher.BlockchainEthereum
			return &b
		case "arbitrum":
			b := blwatcher.BlockchainArbitrum
			return &b
		case "base":
			b := blwatcher.BlockchainBase
			return &b
		case "optimism":
			b := blwatcher.BlockchainOptimism
			return &b
		case "avalanche":
			b := blwatcher.BlockchainAvalanche
			return &b
		default:
			return nil
		}
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		filter := r.URL.Query().Get("chain")
		events, err := eventStorage.GetLatestEventsFiltered(100, parseFilter(filter))
		if err != nil {
			log.Printf("Error getting events: %v", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		data := indexTmplData{
			Events: events,
			Short:  true,
			Filter: strings.ToLower(filter),
		}
		if err := table_tmpl.Execute(w, data); err != nil {
			log.Printf("Error rendering table: %v", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		}
	})

	http.HandleFunc("/all", func(w http.ResponseWriter, r *http.Request) {
		filter := r.URL.Query().Get("chain")
		events, err := eventStorage.GetLatestEventsFiltered(0, parseFilter(filter))
		if err != nil {
			log.Printf("Error getting events: %v", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		data := indexTmplData{
			Events: events,
			Short:  false,
			Filter: strings.ToLower(filter),
		}
		if err := table_tmpl.Execute(w, data); err != nil {
			log.Printf("Error rendering table: %v", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		}
	})

	http.HandleFunc("/address/", func(w http.ResponseWriter, r *http.Request) {
		rawAddress := strings.TrimPrefix(r.URL.Path, "/address/")
		address, err := url.PathUnescape(strings.TrimSuffix(rawAddress, "/"))
		if err != nil || address == "" {
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		normalizedAddress := address

		events, err := eventStorage.GetEventsByAddress(normalizedAddress)
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
			Address: normalizedAddress,
			Events:  events,
		}
		if err := address_tmpl.Execute(w, addressTmplData); err != nil {
			log.Printf("Error rendering address page: %v", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		}
	})

	http.HandleFunc("/rss", func(w http.ResponseWriter, r *http.Request) {
		feed := &feeds.Feed{
			Title:       "Blacklist events of USDT/USDC on Ethereum, Arbitrum and Tron",
			Link:        &feeds.Link{Href: "https://bl.dzen.ws/rss", Rel: "self"},
			Description: "Latest blacklist events of USDT/USDC on Ethereum, Arbitrum and Tron networks",
		}

		events, err := eventStorage.GetLatestEvents(100)
		if err != nil {
			log.Printf("Error getting events for RSS: %v", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}

		for _, e := range events {
			feed.Items = append(feed.Items, &feeds.Item{
				Title:       fmt.Sprintf("[%s] %s %s %s %s", e.Blockchain, e.Type, e.Address, e.Contract.Symbol, e.Date),
				Link:        &feeds.Link{Href: fmt.Sprintf("https://bl.dzen.ws/address/%s", e.Address)},
				Description: fmt.Sprintf("[%s] %s %s %s %s", e.Blockchain, e.Type, e.Address, e.Contract.Symbol, e.Date),
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
		if _, err := w.Write([]byte(rss)); err != nil {
			log.Printf("Error writing RSS response: %v", err)
		}
	})

	log.Printf("Starting server on %s", server.Addr)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal("Server failed to start:", err)
	}
}
