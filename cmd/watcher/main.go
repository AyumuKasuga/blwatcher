package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	"blwatcher"
	"blwatcher/internal"

	"github.com/getsentry/sentry-go"
)

func main() {
	sentryDSN := os.Getenv("SENTRY_DSN")
	if sentryDSN != "" {
		err := sentry.Init(sentry.ClientOptions{
			Dsn: sentryDSN,
		})
		if err != nil {
			log.Printf("Failed to initialize Sentry: %v", err)
		} else {
			defer sentry.Flush(2 * time.Second)
			defer sentry.Recover()
		}
	}

	ethNodeURL := os.Getenv("ETH_NODE_URL")
	if ethNodeURL == "" {
		panic("ETH_NODE_URL is not set")
	}
	arbitrumNodeURL := os.Getenv("ARBITRUM_NODE_URL")
	baseNodeURL := os.Getenv("BASE_NODE_URL")
	optimismNodeURL := os.Getenv("OPTIMISM_NODE_URL")
	avalancheNodeURL := os.Getenv("AVALANCHE_NODE_URL")
	polygonNodeURL := os.Getenv("POLYGON_NODE_URL")
	zksyncNodeURL := os.Getenv("ZKSYNC_NODE_URL")
	connString := os.Getenv("DATABASE_URL")
	if connString == "" {
		connString = "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"
	}
	ctx := context.Background()
	ethContracts := []blwatcher.Contract{
		blwatcher.AddressContractMap[blwatcher.USDTContractAddress],
		blwatcher.AddressContractMap[blwatcher.USDCContractAddress],
		blwatcher.AddressContractMap[blwatcher.USDTMultiSigContractAddress],
	}
	eventStorage := internal.NewEventStorage(
		ctx,
		connString,
	)
	eventChan := make(chan *blwatcher.Event)
	watchers := []blwatcher.Watcher{
		internal.NewWatcher(ethContracts, ethNodeURL, eventChan, eventStorage),
	}

	if arbitrumNodeURL != "" {
		arbitrumContracts := []blwatcher.Contract{
			blwatcher.AddressContractMap[blwatcher.ArbitrumUSDCContractAddress],
		}
		watchers = append(watchers, internal.NewArbitrumWatcher(arbitrumContracts, arbitrumNodeURL, eventChan, eventStorage))
	} else {
		log.Printf("ARBITRUM_NODE_URL not set, Arbitrum watcher disabled")
	}

	if baseNodeURL != "" {
		baseContracts := []blwatcher.Contract{
			blwatcher.AddressContractMap[blwatcher.BaseUSDCContractAddress],
		}
		watchers = append(watchers, internal.NewBaseWatcher(baseContracts, baseNodeURL, eventChan, eventStorage))
	} else {
		log.Printf("BASE_NODE_URL not set, Base watcher disabled")
	}

	if optimismNodeURL != "" {
		optimismContracts := []blwatcher.Contract{
			blwatcher.AddressContractMap[blwatcher.OptimismUSDCContractAddress],
		}
		watchers = append(watchers, internal.NewOptimismWatcher(optimismContracts, optimismNodeURL, eventChan, eventStorage))
	} else {
		log.Printf("OPTIMISM_NODE_URL not set, Optimism watcher disabled")
	}

	if avalancheNodeURL != "" {
		avaxContracts := []blwatcher.Contract{
			blwatcher.AddressContractMap[blwatcher.AvalancheUSDTContractAddress],
			blwatcher.AddressContractMap[blwatcher.AvalancheUSDCContractAddress],
		}
		watchers = append(watchers, internal.NewAvalancheWatcher(avaxContracts, avalancheNodeURL, eventChan, eventStorage))
	} else {
		log.Printf("AVALANCHE_NODE_URL not set, Avalanche watcher disabled")
	}

	if polygonNodeURL != "" {
		polygonContracts := []blwatcher.Contract{
			blwatcher.AddressContractMap[blwatcher.PolygonUSDCContractAddress],
		}
		watchers = append(watchers, internal.NewPolygonWatcher(polygonContracts, polygonNodeURL, eventChan, eventStorage))
	} else {
		log.Printf("POLYGON_NODE_URL not set, Polygon watcher disabled")
	}

	if zksyncNodeURL != "" {
		zksyncContracts := []blwatcher.Contract{
			blwatcher.AddressContractMap[blwatcher.ZkSyncUSDCContractAddress],
		}
		watchers = append(watchers, internal.NewZkSyncWatcher(zksyncContracts, zksyncNodeURL, eventChan, eventStorage))
	} else {
		log.Printf("ZKSYNC_NODE_URL not set, ZkSync watcher disabled")
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

	wg := sync.WaitGroup{}

	for _, watcher := range watchers {
		wg.Add(1)
		go func(ctx context.Context, watcher blwatcher.Watcher) {
			defer wg.Done()
			err := watcher.Watch(ctx)
			if err != nil {
				if sentryDSN != "" {
					sentry.CaptureException(err)
					sentry.Flush(2 * time.Second)
				}
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
