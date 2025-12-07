package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"sync"

	"blwatcher"
	"blwatcher/internal"
)

func main() {
	ethNodeURL := os.Getenv("ETH_NODE_URL")
	if ethNodeURL == "" {
		panic("ETH_NODE_URL is not set")
	}
	connString := os.Getenv("DATABASE_URL")
	if connString == "" {
		connString = "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"
	}
	ctx := context.Background()
	// contracts := []blwatcher.Contract{
	// 	blwatcher.AddressContractMap[blwatcher.USDTContractAddress],
	// 	blwatcher.AddressContractMap[blwatcher.USDCContractAddress],
	// 	blwatcher.AddressContractMap[blwatcher.USDTMultiSigContractAddress],
	// }
	eventStorage := internal.NewEventStorage(
		ctx,
		connString,
	)
	eventChan := make(chan *blwatcher.Event)
	watchers := []blwatcher.Watcher{
		// internal.NewWatcher(contracts, ethNodeURL, eventChan, eventStorage),
	}

	if tronNodeURL := os.Getenv("TRON_NODE_URL"); tronNodeURL != "" {
		tronUSDT := blwatcher.TronUSDTContract
		if override := os.Getenv("TRON_USDT_CONTRACT"); override != "" {
			tronUSDT.Address = override
		}
		tronContracts := []blwatcher.Contract{tronUSDT}

		var tronStartBlock uint64
		if startStr := os.Getenv("TRON_START_BLOCK"); startStr != "" {
			if parsed, err := strconv.ParseUint(startStr, 10, 64); err == nil {
				tronStartBlock = parsed
			} else {
				log.Printf("Invalid TRON_START_BLOCK value %q: %v", startStr, err)
			}
		}

		tronAPIKey := os.Getenv("TRON_API_KEY")

		watchers = append(watchers, internal.NewTronWatcher(tronContracts, tronNodeURL, tronAPIKey, tronStartBlock, eventChan, eventStorage))
	} else {
		log.Printf("TRON_NODE_URL not set, Tron watcher disabled")
	}

	if polkadotNodeURL := os.Getenv("POLKADOT_NODE_URL"); polkadotNodeURL != "" {
		polkadotAssets := map[uint32]blwatcher.Contract{}
		parseAsset := func(envKey, symbol string) {
			if val := os.Getenv(envKey); val != "" {
				if assetID, err := strconv.ParseUint(val, 10, 32); err == nil {
					polkadotAssets[uint32(assetID)] = blwatcher.Contract{
						Address:    val,
						Symbol:     symbol,
						Blockchain: blwatcher.BlockchainPolkadot,
					}
				} else {
					log.Printf("Invalid %s value %q: %v", envKey, val, err)
				}
			}
		}

		parseAsset("POLKADOT_USDT_ASSET_ID", "USDT")
		parseAsset("POLKADOT_USDC_ASSET_ID", "USDC")

		var polkadotStartBlock uint64
		if startStr := os.Getenv("POLKADOT_START_BLOCK"); startStr != "" {
			if parsed, err := strconv.ParseUint(startStr, 10, 64); err == nil {
				polkadotStartBlock = parsed
			} else {
				log.Printf("Invalid POLKADOT_START_BLOCK value %q: %v", startStr, err)
			}
		}

		polkadotSS58Prefix := uint16(0)
		if prefixStr := os.Getenv("POLKADOT_SS58_PREFIX"); prefixStr != "" {
			if parsed, err := strconv.ParseUint(prefixStr, 10, 16); err == nil {
				polkadotSS58Prefix = uint16(parsed)
			} else {
				log.Printf("Invalid POLKADOT_SS58_PREFIX value %q: %v", prefixStr, err)
			}
		}

		if len(polkadotAssets) == 0 {
			log.Printf("POLKADOT_NODE_URL set but no asset IDs configured, skipping Polkadot watcher")
		} else {
			watchers = append(watchers, internal.NewPolkadotWatcher(polkadotNodeURL, polkadotAssets, polkadotStartBlock, polkadotSS58Prefix, eventChan, eventStorage))
		}
	} else {
		log.Printf("POLKADOT_NODE_URL not set, Polkadot watcher disabled")
	}

	wg := sync.WaitGroup{}

	for _, watcher := range watchers {
		wg.Add(1)
		go func(ctx context.Context, watcher blwatcher.Watcher) {
			defer wg.Done()
			err := watcher.Watch(ctx)
			if err != nil {
				panic(err)
			}
		}(ctx, watcher)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case event := <-eventChan:
			err := eventStorage.Store(event)
			if err != nil {
				panic(err)
			}
		}
	}
}
