package internal

import (
	"blwatcher"
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"log"
)

type EventStorage struct {
	conn *pgxpool.Pool
}

func NewEventStorage(ctx context.Context, connString string) blwatcher.EventStorage {
	dbpool, err := pgxpool.New(context.Background(), connString)
	if err != nil {
		panic(err)
	}
	if err != nil {
		panic(err)
	}
	err = dbpool.Ping(ctx)
	if err != nil {
		panic(err)
	}
	return &EventStorage{
		conn: dbpool,
	}
}

func (s *EventStorage) Store(event *blwatcher.Event) error {
	tx, err := s.conn.Begin(context.Background())
	if err != nil {
		return err
	}
	_, err = s.conn.Exec(context.Background(), `
		INSERT INTO events (date, contract, address, tx_hash, block_number, event_type, amount)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, event.Date, event.Contract.Symbol, event.Address, event.Tx, event.BlockNumber, event.Type, event.Amount)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	err = tx.Commit(context.Background())
	log.Printf("Stored event\t[%s]\t|%s|\t(%s)\n", event.Date, event.Type, event.Address)
	return nil
}

func (s *EventStorage) GetLastEventBlock() (uint64, error) {
	var blockNumber uint64
	err := s.conn.QueryRow(context.Background(), `
		SELECT block_number FROM events ORDER BY date DESC LIMIT 1
	`).Scan(&blockNumber)
	if err == pgx.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return blockNumber - 1, nil // hehe
}

func (s *EventStorage) GetLatestEvents(limit uint64) ([]*blwatcher.Event, error) {
	if limit == 0 {
		limit = 100000 // Whatever, I don't care
	}
	rows, err := s.conn.Query(context.Background(), `
		SELECT date, contract, address, tx_hash, block_number, event_type, amount FROM events ORDER BY date DESC LIMIT $1
	`, limit)
	if err == pgx.ErrNoRows {
		return []*blwatcher.Event{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []*blwatcher.Event{}
	for rows.Next() {
		var e blwatcher.Event
		if err := rows.Scan(&e.Date, &e.Contract.Symbol, &e.Address, &e.Tx, &e.BlockNumber, &e.Type, &e.Amount); err != nil {
			return nil, err
		}
		entries = append(entries, &e)
	}
	return entries, nil
}
