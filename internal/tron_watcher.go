package internal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/mr-tron/base58/base58"

	"blwatcher"
)

type tronWatcher struct {
	apiURL         string
	httpClient     *http.Client
	apiKey         string
	startBlock     uint64
	usdtAddressHex string
	contractMap    map[string]blwatcher.Contract
	contractAbiMap map[string]abi.ABI
	eventChan      chan *blwatcher.Event
	eventStorage   blwatcher.EventStorage
}

type tronBlockHeader struct {
	RawData struct {
		Number    uint64 `json:"number"`
		Timestamp int64  `json:"timestamp"`
	} `json:"raw_data"`
}

type tronBlock struct {
	BlockHeader tronBlockHeader `json:"block_header"`
}

type tronNowBlock struct {
	BlockHeader tronBlockHeader `json:"block_header"`
}

type tronEvent struct {
	TransactionID   string            `json:"transaction_id"`
	BlockNumber     uint64            `json:"block_number"`
	BlockTimestamp  int64             `json:"block_timestamp"`
	EventName       string            `json:"event_name"`
	ContractAddress string            `json:"contract_address"`
	Result          map[string]string `json:"result"`
}

type tronEventsResponse struct {
	Data []tronEvent `json:"data"`
	Meta struct {
		PageSize    int    `json:"page_size"`
		Fingerprint string `json:"fingerprint"`
	} `json:"meta"`
}

type tronConstantResult struct {
	ConstantResult []string `json:"constant_result"`
	Result         struct {
		Result  bool   `json:"result"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"result"`
}

func NewTronWatcher(
	contracts []blwatcher.Contract,
	apiURL string,
	apiKey string,
	startBlock uint64,
	eventChan chan *blwatcher.Event,
	eventStorage blwatcher.EventStorage,
) blwatcher.Watcher {
	contractMap := make(map[string]blwatcher.Contract, len(contracts))
	contractAbiMap := make(map[string]abi.ABI, len(contracts))

	var usdtAddressHex string
	for _, contract := range contracts {
		normalizedAddr, err := normalizeTronHex(contract.Address)
		if err != nil {
			panic(err)
		}
		contract.Address = normalizedAddr
		if contract.Blockchain == "" {
			contract.Blockchain = blwatcher.BlockchainTron
		}
		if strings.EqualFold(contract.Symbol, "USDT") && usdtAddressHex == "" {
			usdtAddressHex = normalizedAddr
		}
		contractABI, err := abi.JSON(strings.NewReader(contract.AbiJSON))
		if err != nil {
			panic(err)
		}
		contractMap[normalizedAddr] = contract
		contractAbiMap[normalizedAddr] = contractABI
	}

	return &tronWatcher{
		apiURL:         strings.TrimRight(apiURL, "/"),
		httpClient:     &http.Client{Timeout: 10 * time.Second},
		apiKey:         apiKey,
		startBlock:     startBlock,
		usdtAddressHex: usdtAddressHex,
		contractMap:    contractMap,
		contractAbiMap: contractAbiMap,
		eventChan:      eventChan,
		eventStorage:   eventStorage,
	}
}

func (w *tronWatcher) Watch(ctx context.Context) error {
	fromBlock, err := w.eventStorage.GetLastEventBlock(blwatcher.BlockchainTron)
	if err != nil {
		return err
	}

	var lastSeenBlock uint64
	if fromBlock > 0 {
		lastSeenBlock = fromBlock
	} else if w.startBlock > 0 {
		lastSeenBlock = w.startBlock
	} else {
		latest, err := w.getLatestBlockNumber(ctx)
		if err != nil {
			return err
		}
		lastSeenBlock = latest
	}

	lastSeenTs := w.blockTimestamp(ctx, lastSeenBlock)
	log.Printf("[T] Start watching Tron events from block %d (min_ts=%d)\n", lastSeenBlock, lastSeenTs)

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		maxSeenBlock := lastSeenBlock
		maxSeenTs := lastSeenTs

		for addr, contract := range w.contractMap {
			for _, evName := range w.contractEventNames(contract) {
				events, err := w.fetchEvents(ctx, addr, evName, lastSeenTs)
				if err != nil {
					log.Printf("[T] failed to fetch events for %s %s: %v", contract.Symbol, evName, err)
					continue
				}
				for _, ev := range events {
					if ev.BlockNumber <= lastSeenBlock {
						continue
					}
					if err := w.emitEvent(ev, contract); err != nil {
						log.Printf("[T] failed to process event %+v: %v", ev, err)
						continue
					}
					if ev.BlockNumber > maxSeenBlock {
						maxSeenBlock = ev.BlockNumber
					}
					if ev.BlockTimestamp > maxSeenTs {
						maxSeenTs = ev.BlockTimestamp
					}
				}
			}
		}

		lastSeenBlock = maxSeenBlock
		lastSeenTs = maxSeenTs
		time.Sleep(time.Minute)
	}
}

func (w *tronWatcher) emitEvent(ev tronEvent, contract blwatcher.Contract) error {
	eventType, ok := w.eventTypeForName(ev.EventName)
	if !ok {
		return nil
	}

	address := w.extractAddress(ev)
	if address == "" {
		return fmt.Errorf("unable to extract address from event %s", ev.EventName)
	}

	var amount int64
	if eventType == blwatcher.DestroyBlackFundsEvent {
		amount = w.extractAmount(ev)
	}

	w.eventChan <- &blwatcher.Event{
		Blockchain:  blwatcher.BlockchainTron,
		Date:        time.UnixMilli(ev.BlockTimestamp),
		Contract:    contract,
		Address:     address,
		Tx:          ev.TransactionID,
		BlockNumber: ev.BlockNumber,
		Type:        eventType,
		Amount:      amount,
	}
	return nil
}

func (w *tronWatcher) contractEventNames(contract blwatcher.Contract) []string {
	switch strings.ToLower(contract.Symbol) {
	case "usdt":
		return []string{
			"AddedBlackList",
			"RemovedBlackList",
			"DestroyedBlackFunds",
			"Blacklisted",
			"UnBlacklisted",
		}
	default:
		return []string{}
	}
}

func (w *tronWatcher) eventTypeForName(name string) (blwatcher.EventType, bool) {
	switch name {
	case "AddedBlackList", "Blacklisted":
		return blwatcher.AddBlacklistEvent, true
	case "RemovedBlackList", "UnBlacklisted":
		return blwatcher.RemoveBlacklistEvent, true
	case "DestroyedBlackFunds":
		return blwatcher.DestroyBlackFundsEvent, true
	default:
		return "", false
	}
}

func (w *tronWatcher) extractAddress(ev tronEvent) string {
	possibleKeys := []string{"_user", "_blackListedUser", "user", "account", "addr", "address", "from"}
	for _, key := range possibleKeys {
		if val, ok := ev.Result[key]; ok && val != "" {
			return tronDisplayAddress(val)
		}
	}
	return ""
}

func (w *tronWatcher) extractAmount(ev tronEvent) int64 {
	for _, key := range []string{"_balance", "balance", "amount"} {
		if val, ok := ev.Result[key]; ok && val != "" {
			if n, err := strconv.ParseInt(val, 10, 64); err == nil {
				return n
			}
			if bi, ok := new(big.Int).SetString(strings.TrimPrefix(val, "0x"), 16); ok {
				return bi.Int64()
			}
		}
	}
	return 0
}

func (w *tronWatcher) fetchEvents(ctx context.Context, contractAddr string, eventName string, minTs int64) ([]tronEvent, error) {
	var all []tronEvent
	fingerprint := ""
	apiContractAddr := tronAPIContractAddress(contractAddr)
	if apiContractAddr == "" {
		apiContractAddr = contractAddr
	}
	for {
		eventsResp := tronEventsResponse{}

		q := url.Values{}
		q.Set("event_name", eventName)
		q.Set("only_confirmed", "true")
		q.Set("order_by", "block_timestamp,asc")
		q.Set("limit", "20")
		if minTs > 0 {
			q.Set("min_block_timestamp", strconv.FormatInt(minTs, 10))
		}
		if fingerprint != "" {
			q.Set("fingerprint", fingerprint)
		}

		path := fmt.Sprintf("/v1/contracts/%s/events?%s", apiContractAddr, q.Encode())
		if err := w.get(ctx, path, &eventsResp); err != nil {
			return nil, err
		}

		all = append(all, eventsResp.Data...)

		if eventsResp.Meta.Fingerprint == "" || len(eventsResp.Data) == 0 {
			break
		}
		fingerprint = eventsResp.Meta.Fingerprint
	}
	return all, nil
}

func (w *tronWatcher) getLatestBlockNumber(ctx context.Context) (uint64, error) {
	var now tronNowBlock
	err := w.post(ctx, "/wallet/getnowblock", map[string]interface{}{
		"visible": true,
	}, &now)
	if err != nil {
		return 0, err
	}
	return now.BlockHeader.RawData.Number, nil
}

func (w *tronWatcher) getBlockByNumber(ctx context.Context, num uint64) (*tronBlock, error) {
	var block tronBlock
	err := w.post(ctx, "/wallet/getblockbynum", map[string]interface{}{
		"num":     num,
		"visible": true,
	}, &block)
	if err != nil {
		return nil, err
	}
	return &block, nil
}

func (w *tronWatcher) blockTimestamp(ctx context.Context, blockNumber uint64) int64 {
	if blockNumber == 0 {
		return 0
	}
	block, err := w.getBlockByNumber(ctx, blockNumber)
	if err != nil || block == nil {
		return 0
	}
	return block.BlockHeader.RawData.Timestamp
}

func (w *tronWatcher) post(ctx context.Context, path string, payload interface{}, result interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.apiURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if w.apiKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", w.apiKey)
	}

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("[T] tron api %s returned status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return json.NewDecoder(resp.Body).Decode(result)
}

func (w *tronWatcher) get(ctx context.Context, path string, result interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.apiURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if w.apiKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", w.apiKey)
	}

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("[T] tron api %s returned status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return json.NewDecoder(resp.Body).Decode(result)
}

func normalizeTronHex(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", fmt.Errorf("empty tron address")
	}

	if strings.HasPrefix(addr, "T") || strings.HasPrefix(addr, "t") || strings.HasPrefix(addr, "D") || strings.HasPrefix(addr, "d") {
		decoded, err := base58.Decode(addr)
		if err != nil {
			return "", fmt.Errorf("invalid base58 tron address: %w", err)
		}
		if len(decoded) < 5 {
			return "", fmt.Errorf("invalid base58 tron address")
		}
		payload := decoded[:len(decoded)-4]
		checksum := decoded[len(decoded)-4:]
		hash := sha256.Sum256(payload)
		hash = sha256.Sum256(hash[:])
		if !bytes.Equal(hash[:4], checksum) {
			return "", fmt.Errorf("invalid tron address checksum")
		}
		if len(payload) != 21 {
			return "", fmt.Errorf("unexpected payload length for tron address")
		}
		addr = strings.ToLower(hex.EncodeToString(payload))
	} else {
		addr = strings.TrimPrefix(strings.ToLower(addr), "0x")
	}

	return addr, nil
}

func tronDisplayAddress(addrHex string) string {
	addrHex = strings.TrimPrefix(strings.ToLower(addrHex), "0x")
	if addrHex == "" {
		return addrHex
	}
	if len(addrHex) == 40 {
		addrHex = "41" + addrHex
	}
	addrBytes, err := hex.DecodeString(addrHex)
	if err != nil {
		return "0x" + addrHex
	}
	check := sha256.Sum256(addrBytes)
	check = sha256.Sum256(check[:])
	full := append(addrBytes, check[:4]...)
	return base58.Encode(full)
}

func tronAPIContractAddress(addrHex string) string {
	addrHex = strings.TrimPrefix(strings.ToLower(addrHex), "0x")
	if addrHex == "" {
		return ""
	}
	if len(addrHex) == 40 {
		addrHex = "41" + addrHex
	}
	addrBytes, err := hex.DecodeString(addrHex)
	if err != nil {
		return ""
	}
	check := sha256.Sum256(addrBytes)
	check = sha256.Sum256(check[:])
	full := append(addrBytes, check[:4]...)
	return base58.Encode(full)
}
