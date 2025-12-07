package internal

import (
	"blwatcher"
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	gsrpc "github.com/centrifuge/go-substrate-rpc-client/v4"
	"github.com/centrifuge/go-substrate-rpc-client/v4/registry"
	"github.com/centrifuge/go-substrate-rpc-client/v4/registry/parser"
	"github.com/centrifuge/go-substrate-rpc-client/v4/types"
	"github.com/centrifuge/go-substrate-rpc-client/v4/types/codec"
	"github.com/vedhavyas/go-subkey/v2"
	"golang.org/x/crypto/blake2b"
)

type polkadotWatcher struct {
	nodeURL        string
	assets         map[uint32]blwatcher.Contract
	startBlock     uint64
	ss58Prefix     uint16
	pollInterval   time.Duration
	eventChan      chan *blwatcher.Event
	eventStorage   blwatcher.EventStorage
	metadataBySpec map[uint32]*types.Metadata
	eventRegistry  map[uint32]registry.EventRegistry
	eventsKey      *types.StorageKey
	timestampKey   *types.StorageKey
}

func NewPolkadotWatcher(
	nodeURL string,
	assets map[uint32]blwatcher.Contract,
	startBlock uint64,
	ss58Prefix uint16,
	eventChan chan *blwatcher.Event,
	eventStorage blwatcher.EventStorage,
) blwatcher.Watcher {
	return &polkadotWatcher{
		nodeURL:        strings.TrimSpace(nodeURL),
		assets:         assets,
		startBlock:     startBlock,
		ss58Prefix:     ss58Prefix,
		pollInterval:   6 * time.Second,
		eventChan:      eventChan,
		eventStorage:   eventStorage,
		metadataBySpec: make(map[uint32]*types.Metadata),
		eventRegistry:  make(map[uint32]registry.EventRegistry),
	}
}

func (w *polkadotWatcher) Watch(ctx context.Context) error {
	api, err := gsrpc.NewSubstrateAPI(w.nodeURL)
	if err != nil {
		return err
	}

	fromBlock := w.startBlock
	lastSeen, err := w.eventStorage.GetLastEventBlock(blwatcher.BlockchainPolkadot)
	if err != nil {
		return err
	}
	if lastSeen > 0 {
		fromBlock = lastSeen
	}
	if fromBlock == 0 {
		head, err := api.RPC.Chain.GetFinalizedHead()
		if err != nil {
			return err
		}
		header, err := api.RPC.Chain.GetHeader(head)
		if err != nil {
			return err
		}
		fromBlock = uint64(header.Number)
		log.Printf("[P] POLKADOT_START_BLOCK not set and no previous events, starting from latest finalized block %d — history before this will be skipped", fromBlock)
	}

	log.Printf("[P] Polkadot start params: fromBlock=%d (lastSeen=%d startBlock=%d)", fromBlock, lastSeen, w.startBlock)
	log.Printf("[P] Start Polkadot catch-up from block %d\n", fromBlock)

	if err := w.catchUp(ctx, api, fromBlock); err != nil {
		return err
	}

	// Prepare events storage key for subscriptions using latest metadata.
	eventsKey, err := w.eventsStorageKey(api, types.Hash{})
	if err != nil {
		return err
	}
	w.eventsKey = &eventsKey
	timestampKey, err := w.timestampStorageKey(api, types.Hash{})
	if err != nil {
		return err
	}
	w.timestampKey = &timestampKey

	return w.subscribe(ctx, api)
}

func (w *polkadotWatcher) catchUp(ctx context.Context, api *gsrpc.SubstrateAPI, fromBlock uint64) error {
	head, err := api.RPC.Chain.GetFinalizedHead()
	if err != nil {
		return err
	}
	headHeader, err := api.RPC.Chain.GetHeader(head)
	if err != nil {
		return err
	}
	target := uint64(headHeader.Number)

	if w.eventsKey == nil || w.timestampKey == nil {
		eventsKey, err := w.eventsStorageKey(api, head)
		if err != nil {
			return err
		}
		tsKey, err := w.timestampStorageKey(api, head)
		if err != nil {
			return err
		}
		w.eventsKey = &eventsKey
		w.timestampKey = &tsKey
	}

	const chunkSize uint64 = 200
	for start := fromBlock; start <= target; start += chunkSize {
		end := start + chunkSize - 1
		if end > target {
			end = target
		}
		startHash, err := api.RPC.Chain.GetBlockHash(start)
		if err != nil {
			log.Printf("[P] waiting for block %d: %v", start, err)
			if !w.sleep(ctx, w.pollInterval) {
				return nil
			}
			start -= chunkSize
			continue
		}
		endHash, err := api.RPC.Chain.GetBlockHash(end)
		if err != nil {
			log.Printf("[P] waiting for block %d: %v", end, err)
			if !w.sleep(ctx, w.pollInterval) {
				return nil
			}
			start -= chunkSize
			continue
		}

		changeSets, err := api.RPC.State.QueryStorage([]types.StorageKey{*w.eventsKey, *w.timestampKey}, startHash, endHash)
		if err != nil {
			log.Printf("[P] query storage %d-%d failed: %v, falling back to per-block", start, end, err)
			for b := start; b <= end; b++ {
				select {
				case <-ctx.Done():
					return nil
				default:
				}
				hash, herr := api.RPC.Chain.GetBlockHash(b)
				if herr != nil {
					log.Printf("[P] failed to get block hash %d: %v", b, herr)
					continue
				}
				if perr := w.processBlockByHash(api, hash); perr != nil {
					log.Printf("[P] failed to process block %d: %v", b, perr)
				}
			}
			continue
		}
		log.Printf("[P] catch-up chunk %d-%d (changes=%d)", start, end, len(changeSets))
		for _, cs := range changeSets {
			if err := w.processChangeSet(api, cs); err != nil {
				log.Printf("[P] failed to process block %s: %v", cs.Block.Hex(), err)
			}
		}
	}
	return nil
}

func (w *polkadotWatcher) subscribe(ctx context.Context, api *gsrpc.SubstrateAPI) error {
	if w.eventsKey == nil {
		return fmt.Errorf("events storage key not initialized")
	}
	if w.timestampKey == nil {
		key, err := w.timestampStorageKey(api, types.Hash{})
		if err != nil {
			return err
		}
		w.timestampKey = &key
	}
	sub, err := api.RPC.State.SubscribeStorageRaw([]types.StorageKey{*w.eventsKey})
	if err != nil {
		return err
	}
	defer sub.Unsubscribe()

	log.Printf("[P] Subscribed to Polkadot events")

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-sub.Err():
			return err
		case changeSet, ok := <-sub.Chan():
			if !ok {
				return nil
			}
			if err := w.processChangeSet(api, changeSet); err != nil {
				log.Printf("[P] failed to process block %s: %v", changeSet.Block.Hex(), err)
			}
		}
	}
}

func (w *polkadotWatcher) emitAssetEvent(assetID types.U32, who types.AccountID, phase types.Phase, block *types.SignedBlock, blockHash types.Hash, blockTime time.Time, eventType blwatcher.EventType) {
	contract, ok := w.assets[uint32(assetID)]
	if !ok {
		return
	}

	address := subkey.SS58Encode(who[:], w.ss58Prefix)

	txHash := blockHash.Hex()
	if phase.IsApplyExtrinsic {
		idx := uint32(phase.AsApplyExtrinsic)
		if int(idx) < len(block.Block.Extrinsics) {
			if hash, err := w.extrinsicHash(block.Block.Extrinsics[idx]); err == nil {
				txHash = hash
			} else {
				txHash = fmt.Sprintf("%s-%d", blockHash.Hex(), idx)
			}
		}
	}

	if contract.Address == "" {
		contract.Address = strconv.FormatUint(uint64(assetID), 10)
	}
	contract.Blockchain = blwatcher.BlockchainPolkadot

	w.eventChan <- &blwatcher.Event{
		Blockchain:  blwatcher.BlockchainPolkadot,
		Date:        blockTime,
		Contract:    contract,
		Address:     address,
		Tx:          txHash,
		BlockNumber: uint64(block.Block.Header.Number),
		Type:        eventType,
	}
}

func (w *polkadotWatcher) metadataForBlock(api *gsrpc.SubstrateAPI, blockHash types.Hash) (*types.Metadata, uint32, error) {
	if blockHash == (types.Hash{}) {
		head, err := api.RPC.Chain.GetFinalizedHead()
		if err != nil {
			return nil, 0, err
		}
		blockHash = head
	}

	runtime, err := api.RPC.State.GetRuntimeVersion(blockHash)
	if err != nil {
		return nil, 0, err
	}
	spec := uint32(runtime.SpecVersion)
	if meta, ok := w.metadataBySpec[spec]; ok {
		return meta, spec, nil
	}

	meta, err := api.RPC.State.GetMetadata(blockHash)
	if err != nil {
		return nil, 0, err
	}
	w.metadataBySpec[spec] = meta
	return meta, spec, nil
}

func (w *polkadotWatcher) blockTimestamp(api *gsrpc.SubstrateAPI, blockHash types.Hash, meta *types.Metadata) (time.Time, error) {
	key, err := types.CreateStorageKey(meta, "Timestamp", "Now")
	if err != nil {
		return time.Time{}, err
	}

	var moment types.Moment
	found, err := api.RPC.State.GetStorage(key, &moment, blockHash)
	if err != nil {
		return time.Time{}, err
	}
	if !found {
		return time.Time{}, fmt.Errorf("timestamp not found for block %s", blockHash.Hex())
	}

	return moment.Time.UTC(), nil
}

func (w *polkadotWatcher) processBlockByHash(api *gsrpc.SubstrateAPI, blockHash types.Hash) error {
	meta, spec, err := w.metadataForBlock(api, blockHash)
	if err != nil {
		return err
	}

	eventsKey, err := types.CreateStorageKey(meta, "System", "Events")
	if err != nil {
		return err
	}

	block, err := api.RPC.Chain.GetBlock(blockHash)
	if err != nil {
		return err
	}

	blockTime, err := w.blockTimestamp(api, blockHash, meta)
	if err != nil {
		log.Printf("[P] failed to get timestamp for block %s: %v", blockHash.Hex(), err)
		blockTime = time.Now().UTC()
	}
	log.Printf("[P] catch-up processing block %d at %s (hash=%s)", uint64(block.Block.Header.Number), blockTime.UTC().Format(time.RFC3339), blockHash.Hex())

	raw, err := api.RPC.State.GetStorageRaw(eventsKey, blockHash)
	if err != nil {
		return err
	}
	if raw == nil || len(*raw) == 0 {
		return nil
	}

	return w.decodeAndEmit(meta, spec, block, blockHash, blockTime, *raw)
}

func (w *polkadotWatcher) processChangeSet(api *gsrpc.SubstrateAPI, cs types.StorageChangeSet) error {
	if cs.Block == (types.Hash{}) {
		return nil
	}

	var ts *types.Moment
	for _, ch := range cs.Changes {
		if !ch.HasStorageData {
			continue
		}
		if w.timestampKey != nil && ch.StorageKey.Hex() == w.timestampKey.Hex() {
			var m types.Moment
			if err := codec.Decode(ch.StorageData, &m); err == nil {
				ts = &m
			}
		}
	}

	return w.processEventData(api, cs.Block, cs.Changes, ts)
}

func (w *polkadotWatcher) processEventData(api *gsrpc.SubstrateAPI, blockHash types.Hash, changes []types.KeyValueOption, ts *types.Moment) error {
	if blockHash == (types.Hash{}) {
		return nil
	}
	meta, spec, err := w.metadataForBlock(api, blockHash)
	if err != nil {
		return err
	}

	block, err := api.RPC.Chain.GetBlock(blockHash)
	if err != nil {
		return err
	}

	var blockTime time.Time
	if ts != nil {
		blockTime = ts.Time.UTC()
	} else {
		blockTime, err = w.blockTimestamp(api, blockHash, meta)
		if err != nil {
			log.Printf("[P] failed to get timestamp for block %s: %v", blockHash.Hex(), err)
			blockTime = time.Now().UTC()
		}
	}

	for _, ch := range changes {
		if !ch.HasStorageData {
			continue
		}
		if w.eventsKey != nil && ch.StorageKey.Hex() == w.eventsKey.Hex() {
			return w.decodeAndEmit(meta, spec, block, blockHash, blockTime, ch.StorageData)
		}
	}

	return nil
}

func (w *polkadotWatcher) decodeAndEmit(meta *types.Metadata, spec uint32, block *types.SignedBlock, blockHash types.Hash, blockTime time.Time, raw types.StorageDataRaw) error {
	if len(raw) == 0 {
		return nil
	}

	reg, err := w.eventRegistryForSpec(meta, spec)
	if err != nil {
		return err
	}

	events, err := parser.NewEventParser().ParseEvents(reg, &raw)
	if err != nil {
		return err
	}

	for _, ev := range events {
		switch ev.Name {
		case "Assets.Frozen", "Assets.Blocked":
			w.handleAssetEvent(ev, block, blockHash, blockTime, blwatcher.AddBlacklistEvent)
		case "Assets.Thawed":
			w.handleAssetEvent(ev, block, blockHash, blockTime, blwatcher.RemoveBlacklistEvent)
		default:
			continue
		}
	}

	return nil
}

func (w *polkadotWatcher) handleAssetEvent(ev *parser.Event, block *types.SignedBlock, blockHash types.Hash, blockTime time.Time, eventType blwatcher.EventType) {
	assetID, who, ok := w.extractAssetFields(ev.Fields)
	if !ok {
		log.Printf("[P] unable to parse asset event %s fields: %s", ev.Name, w.describeFields(ev.Fields))
		return
	}

	var phase types.Phase
	if ev.Phase != nil {
		phase = *ev.Phase
	}

	w.emitAssetEvent(types.U32(assetID), who, phase, block, blockHash, blockTime, eventType)
}

func (w *polkadotWatcher) extractAssetFields(fields registry.DecodedFields) (uint32, types.AccountID, bool) {
	var assetID uint32
	var who types.AccountID
	for _, f := range fields {
		name := strings.ToLower(f.Name)
		switch name {
		case "assetid", "asset_id", "id", "asset":
			switch v := f.Value.(type) {
			case types.U32:
				assetID = uint32(v)
			case uint32:
				assetID = v
			}
		case "who", "account", "owner", "user":
			if aid, ok := w.decodeAccountID(f.Value); ok {
				who = aid
			}
		default:
			// Fallback by type if names are missing
			switch v := f.Value.(type) {
			case types.U32:
				if assetID == 0 {
					assetID = uint32(v)
				}
			case uint32:
				if assetID == 0 {
					assetID = v
				}
			case types.U64:
				if assetID == 0 {
					assetID = uint32(v)
				}
			case types.AccountID:
				if who == (types.AccountID{}) {
					who = v
				}
			case []byte:
				if who == (types.AccountID{}) {
					if aid, ok := w.bytesToAccountID(v); ok {
						who = aid
					}
				}
			case types.Bytes:
				if who == (types.AccountID{}) {
					if aid, ok := w.bytesToAccountID([]byte(v)); ok {
						who = aid
					}
				}
			case registry.DecodedFields:
				if who == (types.AccountID{}) {
					if aid, ok := w.findAccountID(v); ok {
						who = aid
					}
				}
			default:
				if who == (types.AccountID{}) {
					if aid, ok := w.decodeAccountID(v); ok {
						who = aid
					}
				}
			}
			// As a last attempt per-field, try flattening this field's value into bytes.
			if who == (types.AccountID{}) {
				if b := w.interfacesToBytes(f.Value); len(b) >= 32 {
					if aid, ok := w.bytesToAccountID(b[:32]); ok {
						who = aid
						log.Printf("[P] fallback who from field %s flattened bytes len=%d", f.Name, len(b))
					}
				}
			}
		}
	}
	// Last-resort fallback: try to coerce any remaining nested bytes
	if who == (types.AccountID{}) {
		if fallback := w.interfacesToBytes(fields); len(fallback) >= 32 {
			if aid, ok := w.bytesToAccountID(fallback[:32]); ok {
				who = aid
				log.Printf("[P] fallback who from raw bytes len=%d", len(fallback))
			}
		}
	}
	if assetID == 0 || who == (types.AccountID{}) {
		log.Printf("[P] failed to extract who; raw bytes len=%d", len(w.interfacesToBytes(fields)))
		return 0, types.AccountID{}, false
	}
	log.Printf("[P] matched asset event assetID=%d who=%s", assetID, subkey.SS58Encode(who[:], w.ss58Prefix))
	return assetID, who, true
}

func (w *polkadotWatcher) describeFields(fields registry.DecodedFields) string {
	return w.describeFieldsRecursive(fields, 0)
}

func (w *polkadotWatcher) bytesToAccountID(b []byte) (types.AccountID, bool) {
	if len(b) != 32 {
		return types.AccountID{}, false
	}
	var aid types.AccountID
	copy(aid[:], b)
	return aid, true
}

func (w *polkadotWatcher) findAccountID(fields registry.DecodedFields) (types.AccountID, bool) {
	for _, f := range fields {
		switch v := f.Value.(type) {
		case types.AccountID:
			return v, true
		case registry.DecodedFields:
			if aid, ok := w.findAccountID(v); ok {
				return aid, true
			}
		case []byte:
			if aid, ok := w.bytesToAccountID(v); ok {
				return aid, true
			}
		case types.Bytes:
			if aid, ok := w.bytesToAccountID([]byte(v)); ok {
				return aid, true
			}
		default:
			if aid, ok := w.decodeAccountID(v); ok {
				return aid, true
			}
		}
	}
	return types.AccountID{}, false
}

func (w *polkadotWatcher) accountIDFromAny(v interface{}) (types.AccountID, bool) {
	switch val := v.(type) {
	case string:
		if b, err := codec.HexDecodeString(strings.TrimSpace(val)); err == nil {
			return w.bytesToAccountID(b)
		}
	case fmt.Stringer:
		if b, err := codec.HexDecodeString(strings.TrimSpace(val.String())); err == nil {
			return w.bytesToAccountID(b)
		}
	case types.H256:
		return w.bytesToAccountID(val[:])
	case types.Hash:
		return w.bytesToAccountID(val[:])
	case []types.U8:
		return w.bytesToAccountID(w.interfacesToBytes(val))
	case []interface{}:
		return w.bytesToAccountID(w.interfacesToBytes(val))
	case []uint8:
		return w.bytesToAccountID(val)
	case []uint:
		return w.bytesToAccountID(w.interfacesToBytes(val))
	case []uint32:
		return w.bytesToAccountID(w.interfacesToBytes(val))
	case []uint64:
		return w.bytesToAccountID(w.interfacesToBytes(val))
	case [32]byte:
		return w.bytesToAccountID(val[:])
	}
	return types.AccountID{}, false
}

func (w *polkadotWatcher) interfacesToBytes(val interface{}) []byte {
	var out []byte
	switch v := val.(type) {
	case []types.U8:
		for _, u := range v {
			out = append(out, byte(u))
		}
	case []interface{}:
		for _, u := range v {
			switch n := u.(type) {
			case uint8:
				out = append(out, n)
			case uint:
				out = append(out, byte(n))
			case uint32:
				out = append(out, byte(n))
			case uint64:
				out = append(out, byte(n))
			case int:
				out = append(out, byte(n))
			case int64:
				out = append(out, byte(n))
			case float64:
				out = append(out, byte(n))
			case []interface{}:
				out = append(out, w.interfacesToBytes(n)...)
			case []types.U8:
				out = append(out, w.interfacesToBytes(n)...)
			}
		}
	case types.Bytes:
		out = append(out, []byte(v)...)
	case registry.DecodedFields:
		for _, f := range v {
			out = append(out, w.interfacesToBytes(f.Value)...)
		}
	case []uint8:
		out = append(out, v...)
	case []uint:
		for _, u := range v {
			out = append(out, byte(u))
		}
	case []uint32:
		for _, u := range v {
			out = append(out, byte(u))
		}
	case []uint64:
		for _, u := range v {
			out = append(out, byte(u))
		}
	}
	return out
}

func (w *polkadotWatcher) decodeAccountID(v interface{}) (types.AccountID, bool) {
	if aid, ok := v.(types.AccountID); ok {
		return aid, true
	}
	if aid, ok := w.accountIDFromAny(v); ok {
		return aid, true
	}

	encoded, err := codec.Encode(v)
	if err != nil {
		return types.AccountID{}, false
	}
	var aid types.AccountID
	if err := codec.Decode(encoded, &aid); err != nil {
		return types.AccountID{}, false
	}
	return aid, true
}

func (w *polkadotWatcher) describeFieldsRecursive(fields registry.DecodedFields, depth int) string {
	prefix := strings.Repeat(">", depth)
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		switch v := f.Value.(type) {
		case registry.DecodedFields:
			parts = append(parts, fmt.Sprintf("%s%s(%T)=[%s]", prefix, f.Name, f.Value, w.describeFieldsRecursive(v, depth+1)))
		default:
			parts = append(parts, fmt.Sprintf("%s%s(%T)=%v", prefix, f.Name, f.Value, f.Value))
		}
	}
	return strings.Join(parts, "; ")
}

func (w *polkadotWatcher) eventsStorageKey(api *gsrpc.SubstrateAPI, blockHash types.Hash) (types.StorageKey, error) {
	meta, _, err := w.metadataForBlock(api, blockHash)
	if err != nil {
		return nil, err
	}
	return types.CreateStorageKey(meta, "System", "Events")
}

func (w *polkadotWatcher) timestampStorageKey(api *gsrpc.SubstrateAPI, blockHash types.Hash) (types.StorageKey, error) {
	meta, _, err := w.metadataForBlock(api, blockHash)
	if err != nil {
		return nil, err
	}
	return types.CreateStorageKey(meta, "Timestamp", "Now")
}

func (w *polkadotWatcher) extrinsicHash(ext types.Extrinsic) (string, error) {
	encoded, err := codec.Encode(ext)
	if err != nil {
		return "", err
	}
	sum := blake2b.Sum256(encoded)
	return types.NewHash(sum[:]).Hex(), nil
}

func (w *polkadotWatcher) eventRegistryForSpec(meta *types.Metadata, spec uint32) (registry.EventRegistry, error) {
	if reg, ok := w.eventRegistry[spec]; ok {
		return reg, nil
	}
	reg, err := registry.NewFactory().CreateEventRegistry(meta)
	if err != nil {
		return nil, err
	}
	w.eventRegistry[spec] = reg
	return reg, nil
}

func (w *polkadotWatcher) sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
