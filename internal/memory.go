package internal

import (
	"blwatcher"
	"log"
)

type MemEventStorage struct{}

func NewMemEventStorage() blwatcher.EventStorage {
	return &MemEventStorage{}
}

func (e *MemEventStorage) Store(event *blwatcher.Event) error {
	log.Printf("Event Type: %s\n", event.Type)
	log.Printf("Event Date: %s\n", event.Date)
	log.Printf("Event Contract: %s\n", event.Contract.Symbol)
	log.Printf("Event Address: %s\n", event.Address)
	log.Printf("Event Amount: %d\n", event.Amount)
	log.Println("====================================")
	return nil
}

func (e *MemEventStorage) GetLastEventBlock() (uint64, error) {
	return 0, nil
}

func (e *MemEventStorage) GetLatestEvents(limit uint64) ([]*blwatcher.Event, error) {
	return []*blwatcher.Event{}, nil
}
