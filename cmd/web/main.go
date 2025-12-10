package main

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"encoding/xml"
	"sync"

	"github.com/getsentry/sentry-go"
	sentryhttp "github.com/getsentry/sentry-go/http"
	"github.com/gorilla/feeds"

	"blwatcher"
	"blwatcher/internal"
)

func main() {
	ctx := context.Background()
	connString := os.Getenv("DATABASE_URL")

	sentryDSN := os.Getenv("SENTRY_DSN")
	if sentryDSN != "" {
		if err := sentry.Init(sentry.ClientOptions{
			Dsn:              sentryDSN,
			EnableLogs:       true,
			TracesSampleRate: 0.2,
		}); err != nil {
			log.Printf("Failed to initialize Sentry: %v", err)
		} else {
			defer sentry.Flush(2 * time.Second)
			defer sentry.Recover()
		}
	}

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

	sentryMiddleware := sentryhttp.New(sentryhttp.Options{})

	funcMap := template.FuncMap{
		"datetime": func(t time.Time) string {
			return t.Format(time.RFC3339)
		},
		"ageBadge": func(now, t time.Time) template.HTML {
			d := now.Sub(t)
			switch {
			case d <= time.Hour:
				return `<span class="badge bg-danger ms-1">new</span>`
			case d <= 24*time.Hour:
				return `<span class="badge bg-warning text-dark ms-1">recent</span>`
			default:
				days := int(d.Hours() / 24)
				return template.HTML(fmt.Sprintf(`<span class="badge bg-secondary ms-1">%d days ago</span>`, days))
			}
		},
	}

	table_tmpl := template.Must(template.New("table.html").Funcs(funcMap).ParseFiles("templates/table.html"))
	address_tmpl := template.Must(template.New("address.html").Funcs(funcMap).ParseFiles("templates/address.html"))

	type addressTmplData = struct {
		Address string
		Events  []*blwatcher.Event
	}

	type indexTmplData struct {
		Events   []*blwatcher.Event
		Filter   string
		Page     int
		HasPrev  bool
		HasNext  bool
		PrevPage int
		NextPage int
		Now      time.Time
	}

	type urlEntry struct {
		Loc     string `xml:"loc"`
		LastMod string `xml:"lastmod,omitempty"`
	}

	type urlSet struct {
		XMLName xml.Name   `xml:"urlset"`
		Xmlns   string     `xml:"xmlns,attr"`
		Urls    []urlEntry `xml:"url"`
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
		case "polygon":
			b := blwatcher.BlockchainPolygon
			return &b
		case "zksync":
			b := blwatcher.BlockchainZkSync
			return &b
		default:
			return nil
		}
	}

	const pageSize = 200

	var (
		sitemapMu    sync.Mutex
		sitemapLast  int64
	)

	getCachedSitemapID := func() int64 {
		sitemapMu.Lock()
		defer sitemapMu.Unlock()
		return sitemapLast
	}

	setCachedSitemapID := func(lastID int64) {
		sitemapMu.Lock()
		defer sitemapMu.Unlock()
		sitemapLast = lastID
	}

	const sitemapPath = "/tmp/sitemap.xml"

	http.Handle("/", sentryMiddleware.HandleFunc(func(w http.ResponseWriter, r *http.Request) {
		filter := r.URL.Query().Get("chain")
		page := 1
		if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
			page = p
		}
		offset := uint64((page - 1) * pageSize)

		events, err := eventStorage.GetLatestEventsFiltered(pageSize, offset, parseFilter(filter))
		if err != nil {
			log.Printf("Error getting events: %v", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		now := time.Now().UTC()
		data := indexTmplData{
			Events:   events,
			Filter:   strings.ToLower(filter),
			Page:     page,
			HasPrev:  page > 1,
			HasNext:  len(events) == pageSize,
			PrevPage: page - 1,
			NextPage: page + 1,
			Now:      now,
		}
		if err := table_tmpl.Execute(w, data); err != nil {
			log.Printf("Error rendering table: %v", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		}
	}))

	http.Handle("/address/", sentryMiddleware.HandleFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))

	http.Handle("/rss", sentryMiddleware.HandleFunc(func(w http.ResponseWriter, r *http.Request) {
		feed := &feeds.Feed{
			Title:       "Blacklist events of USDT/USDC across multiple blockchains",
			Link:        &feeds.Link{Href: "https://bl.dzen.ws/rss", Rel: "self"},
			Description: "Latest blacklist events of USDT/USDC on Ethereum, Arbitrum, Base, Optimism, Avalanche, Polygon, ZkSync and Tron networks",
		}

		events, err := eventStorage.GetLatestEvents(250)
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
	}))

	http.Handle("/sitemap.xml", sentryMiddleware.HandleFunc(func(w http.ResponseWriter, r *http.Request) {
		latestID, err := eventStorage.GetLatestEventID()
		if err != nil {
			log.Printf("Error getting latest event id: %v", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}

		if cachedID := getCachedSitemapID(); cachedID == latestID {
			if _, err := os.Stat(sitemapPath); err == nil {
				w.Header().Set("Content-Type", "application/xml")
				http.ServeFile(w, r, sitemapPath)
				return
			}
		}

		baseURL := "https://bl.dzen.ws"

		urls := []urlEntry{
			{Loc: baseURL + "/"},
			{Loc: baseURL + "/?page=1&chain=ethereum"},
			{Loc: baseURL + "/?page=1&chain=arbitrum"},
			{Loc: baseURL + "/?page=1&chain=base"},
			{Loc: baseURL + "/?page=1&chain=optimism"},
			{Loc: baseURL + "/?page=1&chain=avalanche"},
			{Loc: baseURL + "/?page=1&chain=polygon"},
			{Loc: baseURL + "/?page=1&chain=zksync"},
			{Loc: baseURL + "/?page=1&chain=tron"},
		}

		addresses, err := eventStorage.GetAllAddressesWithLastDate()
		if err != nil {
			log.Printf("Error getting addresses for sitemap: %v", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		for _, addr := range addresses {
			urls = append(urls, urlEntry{
				Loc:     fmt.Sprintf("%s/address/%s", baseURL, addr.Address),
				LastMod: addr.Date.Format(time.RFC3339),
			})
		}

		us := urlSet{
			Xmlns: "http://www.sitemaps.org/schemas/sitemap/0.9",
			Urls:  urls,
		}

		output, err := xml.MarshalIndent(us, "", "  ")
		if err != nil {
			log.Printf("Error marshaling sitemap: %v", err)
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		// Add XML header
		output = append([]byte(xml.Header), output...)

		if err := os.WriteFile(sitemapPath, output, 0o644); err != nil {
			log.Printf("Error writing sitemap file: %v", err)
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write(output)
			return
		}
		setCachedSitemapID(latestID)

		w.Header().Set("Content-Type", "application/xml")
		http.ServeFile(w, r, sitemapPath)
	}))

	http.Handle("/robots.txt", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("User-agent: *\nAllow: /\nSitemap: https://bl.dzen.ws/sitemap.xml\n"))
	}))
	http.Handle("/healthz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	log.Printf("Starting server on %s", server.Addr)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal("Server failed to start:", err)
	}
}
