package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"blwatcher"
)

type assetHubWatcher struct {
	apiURL       string
	apiKey       string
	assetID      string
	httpClient   *http.Client
	eventChan    chan *blwatcher.Event
	eventStorage blwatcher.EventStorage
	pollInterval time.Duration
	rateLimiter  <-chan time.Time
}

type subscanEventParam struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

type subscanEvent struct {
	EventID        string              `json:"event_id"`
	ModuleID       string              `json:"module_id"`
	Params         []subscanEventParam `json:"params"`
	BlockNum       json.Number         `json:"block_num"`
	BlockTimestamp int64               `json:"block_timestamp"`
	EventIndex     string              `json:"event_index"`
	ExtrinsicHash  string              `json:"extrinsic_hash"`
}

type subscanActivitiesResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Count int            `json:"count"`
		List  []subscanEvent `json:"list"`
	} `json:"data"`
}

func NewAssetHubWatcher(apiURL string, apiKey string, eventChan chan *blwatcher.Event, eventStorage blwatcher.EventStorage) blwatcher.Watcher {
	return &assetHubWatcher{
		apiURL:       strings.TrimRight(apiURL, "/"),
		apiKey:       apiKey,
		assetID:      blwatcher.AssetHubUSDTAssetID,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		eventChan:    eventChan,
		eventStorage: eventStorage,
		pollInterval: 30 * time.Second,
		rateLimiter:  time.Tick(250 * time.Millisecond),
	}
}

func (w *assetHubWatcher) Watch(ctx context.Context) error {
	lastBlock, err := w.eventStorage.GetLastEventBlock(blwatcher.BlockchainAssetHub)
	if err != nil {
		return err
	}
	log.Printf("[AH] Start watching Asset Hub events from block %d\n", lastBlock)

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		newLast, err := w.pollOnce(ctx, lastBlock)
		if err != nil {
			log.Printf("[AH] poll error: %v", err)
		} else if newLast > lastBlock {
			lastBlock = newLast
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (w *assetHubWatcher) pollOnce(ctx context.Context, minBlock uint64) (uint64, error) {
	latest := minBlock
	page := 0

	for {
		log.Printf("[AH][debug] fetching activities page=%d since_block=%d", page, minBlock)
		events, err := w.fetchActivities(ctx, page)
		if err != nil {
			return latest, err
		}
		if len(events) == 0 {
			log.Printf("[AH][debug] no activities on page=%d, stopping", page)
			break
		}

		for _, ev := range events {
			blockNum, err := parseJSONUint(ev.BlockNum)
			if err != nil {
				continue
			}
			if blockNum <= minBlock {
				log.Printf("[AH][debug] hit known block %d (min %d), stopping", blockNum, minBlock)
				return latest, nil
			}

			eventType, ok := w.eventType(ev.EventID)
			if !ok {
				if ev.EventID != "Burned" && ev.EventID != "Issued" {
					log.Printf("[AH][debug] skip event (type) id=%s module=%s idx=%s", ev.EventID, ev.ModuleID, ev.EventIndex)
				}
				continue
			}

			account, ok := w.extractAccount(ev)
			if !ok {
				log.Printf("[AH][debug] skip event (no account) id=%s module=%s idx=%s", ev.EventID, ev.ModuleID, ev.EventIndex)
				continue
			}

			if blockNum > latest {
				latest = blockNum
			}

			evTime := time.Unix(ev.BlockTimestamp, 0).UTC()
			tx := ev.ExtrinsicHash
			if tx == "" {
				tx = ev.EventIndex
			}
			if tx == "" {
				tx = fmt.Sprintf("%d-%s", blockNum, ev.EventID)
			}
			w.eventChan <- &blwatcher.Event{
				Blockchain:  blwatcher.BlockchainAssetHub,
				Date:        evTime,
				Contract:    blwatcher.AssetHubUSDTContract,
				Address:     account,
				Tx:          tx,
				BlockNumber: blockNum,
				Type:        eventType,
			}
		}

		page++
	}
	return latest, nil
}

func (w *assetHubWatcher) fetchActivities(ctx context.Context, page int) ([]subscanEvent, error) {
	select {
	case <-w.rateLimiter:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	body := map[string]interface{}{
		"asset_id": w.assetID,
		"page":     page,
		"row":      100,
		"order":    "desc",
	}

	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.apiURL+"/api/scan/assets/activities", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if w.apiKey != "" {
		req.Header.Set("X-API-Key", w.apiKey)
	}

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusTooManyRequests {
			time.Sleep(time.Second)
		}
		return nil, fmt.Errorf("subscan returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed subscanActivitiesResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	if parsed.Code != 0 {
		return nil, fmt.Errorf("subscan returned code %d: %s", parsed.Code, parsed.Message)
	}

	return parsed.Data.List, nil
}

func (w *assetHubWatcher) extractAccount(ev subscanEvent) (string, bool) {
	if strings.ToLower(ev.ModuleID) != "assets" {
		return "", false
	}

	var (
		assetMatch bool
		account    string
	)

	for i, param := range ev.Params {
		val := rawJSONValue(param.Value)
		lowerType := strings.ToLower(param.Type)

		if i == 0 || strings.Contains(lowerType, "asset") {
			if val == w.assetID {
				assetMatch = true
			}
		}

		if strings.Contains(lowerType, "account") || lowerType == "address" {
			account = val
		}
	}

	if !assetMatch || account == "" {
		return "", false
	}

	return account, true
}

func (w *assetHubWatcher) eventType(eventID string) (blwatcher.EventType, bool) {
	switch strings.ToLower(eventID) {
	case "frozen":
		return blwatcher.AddBlacklistEvent, true
	case "thawed":
		return blwatcher.RemoveBlacklistEvent, true
	default:
		return "", false
	}
}

func parseJSONUint(num json.Number) (uint64, error) {
	s := num.String()
	if s == "" {
		return 0, fmt.Errorf("empty number")
	}

	if v, err := strconv.ParseUint(s, 10, 64); err == nil {
		return v, nil
	}

	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if f < 0 {
		return 0, fmt.Errorf("negative number %v", f)
	}
	return uint64(f), nil
}

func rawJSONValue(v json.RawMessage) string {
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		return strings.TrimSpace(s)
	}

	var n json.Number
	if err := json.Unmarshal(v, &n); err == nil {
		return n.String()
	}

	return strings.Trim(strings.TrimSpace(string(v)), "\"")
}
