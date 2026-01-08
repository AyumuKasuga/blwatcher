package internal

import (
	"blwatcher"
	"context"
	"log"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type EventStorage struct {
	conn *pgxpool.Pool
}

func NewEventStorage(ctx context.Context, connString string) blwatcher.EventStorage {
	dbpool, err := pgxpool.New(context.Background(), connString)
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
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	_, err = tx.Exec(context.Background(), `
		INSERT INTO events (blockchain, date, contract, address, tx_hash, block_number, event_type, amount)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT DO NOTHING
	`, event.Blockchain, event.Date, event.Contract.Symbol, event.Address, event.Tx, event.BlockNumber, event.Type, event.Amount)
	if err != nil {
		return err
	}
	if err := tx.Commit(context.Background()); err != nil {
		return err
	}
	log.Printf("[%s] Stored event\t[%s]\t|%s - %s|\t(%s)\n", strings.ToUpper(string(event.Blockchain)), event.Date, event.Contract.Symbol, event.Type, event.Address)
	return nil
}

func (s *EventStorage) GetLastEventBlock(blockchain blwatcher.Blockchain) (uint64, error) {
	var blockNumber uint64
	err := s.conn.QueryRow(context.Background(), `
		SELECT block_number
		FROM events
		WHERE blockchain = $1
		ORDER BY block_number DESC
		LIMIT 1
	`, blockchain).Scan(&blockNumber)
	if err == pgx.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return blockNumber - 1, nil // hehe
}

func (s *EventStorage) GetLatestEvents(limit uint64) ([]*blwatcher.Event, error) {
	return s.GetLatestEventsFiltered(limit, 0, nil)
}

func (s *EventStorage) GetEventsByAddress(address string) ([]*blwatcher.Event, error) {
	rows, err := s.conn.Query(context.Background(), `
		SELECT blockchain, date, contract, address, tx_hash, block_number, event_type, amount
		FROM events
		WHERE address = $1
		ORDER BY date DESC
	`, address)
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
		if err := rows.Scan(&e.Blockchain, &e.Date, &e.Contract.Symbol, &e.Address, &e.Tx, &e.BlockNumber, &e.Type, &e.Amount); err != nil {
			return nil, err
		}
		e.Contract.Blockchain = e.Blockchain
		entries = append(entries, &e)
	}
	return entries, nil
}

func (s *EventStorage) GetLatestEventsFiltered(limit uint64, offset uint64, blockchain *blwatcher.Blockchain) ([]*blwatcher.Event, error) {
	if limit == 0 {
		limit = 200
	}
	args := []any{limit, offset}
	query := `
		SELECT blockchain, date, contract, address, tx_hash, block_number, event_type, amount
		FROM events
	`
	if blockchain != nil {
		query += "WHERE blockchain = $3\n"
		args = append(args, *blockchain)
	}
	query += "ORDER BY date DESC\nLIMIT $1 OFFSET $2"

	rows, err := s.conn.Query(context.Background(), query, args...)
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
		if err := rows.Scan(&e.Blockchain, &e.Date, &e.Contract.Symbol, &e.Address, &e.Tx, &e.BlockNumber, &e.Type, &e.Amount); err != nil {
			return nil, err
		}
		e.Contract.Blockchain = e.Blockchain
		entries = append(entries, &e)
	}
	return entries, nil
}

func (s *EventStorage) GetLatestEventID() (int64, error) {
	var id int64
	err := s.conn.QueryRow(context.Background(), `
		SELECT id FROM events ORDER BY id DESC LIMIT 1
	`).Scan(&id)
	if err == pgx.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s *EventStorage) GetAllAddressesWithLastDate() ([]blwatcher.AddressLastDate, error) {
	rows, err := s.conn.Query(context.Background(), `
		SELECT address, MAX(date) AS last_date
		FROM events
		GROUP BY address
		ORDER BY last_date DESC
	`)
	if err == pgx.ErrNoRows {
		return []blwatcher.AddressLastDate{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []blwatcher.AddressLastDate
	for rows.Next() {
		var item blwatcher.AddressLastDate
		if err := rows.Scan(&item.Address, &item.Date); err != nil {
			return nil, err
		}
		entries = append(entries, item)
	}
	return entries, nil
}
