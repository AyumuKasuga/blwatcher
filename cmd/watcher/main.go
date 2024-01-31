package main

import (
	"blwatcher"
	"blwatcher/internal"
	"context"
	"os"
	"sync"
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
	contracts := []blwatcher.Contract{
		//blwatcher.AddressContractMap[blwatcher.USDTContractAddress],
		blwatcher.AddressContractMap[blwatcher.USDCContractAddress],
	}
	eventStorage := internal.NewEventStorage(
		ctx,
		connString,
	)
	eventChan := make(chan *blwatcher.Event)
	parser := internal.NewWatcher(contracts, ethNodeURL, eventChan, eventStorage)

	wg := sync.WaitGroup{}

	wg.Add(1)
	go func(ctx context.Context) {
		defer wg.Done()
		err := parser.Watch(ctx)
		if err != nil {
			panic(err)
		}
	}(ctx)

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
