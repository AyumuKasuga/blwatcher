package internal

import (
	"context"
	"fmt"
	"github.com/ethereum/go-ethereum/core/types"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"blwatcher"
)

var addedBlackListSig = crypto.Keccak256Hash([]byte("AddedBlackList(address)"))
var removedBlackListSig = crypto.Keccak256Hash([]byte("RemovedBlackList(address)"))
var destroyedBlackFundsSig = crypto.Keccak256Hash([]byte("DestroyedBlackFunds(address,uint256)"))

type Watcher struct {
	contractAddresses []common.Address
	ethNodeURL        string
	contractAbiMap    map[common.Address]abi.ABI
	eventChan         chan *blwatcher.Event
	eventStorage      blwatcher.EventStorage
}

func NewWatcher(
	contracts []blwatcher.Contract,
	ethNodeURL string,
	eventChan chan *blwatcher.Event,
	eventStorage blwatcher.EventStorage,
) blwatcher.Watcher {
	contractAddresses := make([]common.Address, len(contracts))
	contractAbiMap := make(map[common.Address]abi.ABI, len(contracts))
	for i, contract := range contracts {
		contractAddresses[i] = common.HexToAddress(contract.Address)
		contractAbi, err := abi.JSON(strings.NewReader(contract.AbiJSON))
		if err != nil {
			panic(err)
		}
		contractAbiMap[contractAddresses[i]] = contractAbi
	}
	return &Watcher{
		contractAddresses: contractAddresses,
		ethNodeURL:        ethNodeURL,
		contractAbiMap:    contractAbiMap,
		eventChan:         eventChan,
		eventStorage:      eventStorage,
	}
}

func (w *Watcher) Watch(ctx context.Context) error {
	client, err := ethclient.Dial(w.ethNodeURL)
	if err != nil {
		return err
	}
	fromBlock, err := w.eventStorage.GetLastEventBlock()
	if err != nil {
		return err
	}
	query := ethereum.FilterQuery{
		Addresses: w.contractAddresses,
		Topics: [][]common.Hash{
			{addedBlackListSig, removedBlackListSig, destroyedBlackFundsSig},
		},
		FromBlock: big.NewInt(int64(fromBlock)),
	}

	logChan := make(chan types.Log)

	sub, err := client.SubscribeFilterLogs(ctx, query, logChan)
	if err != nil {
		return err
	}
	defer sub.Unsubscribe()

	go func(ctx context.Context, client *ethclient.Client, fromBlock uint64) {
		err := w.processPastEvents(ctx, client, fromBlock)
		if err != nil {
			panic(err)
		}
	}(ctx, client, fromBlock)

	log.Printf("Start watching from block %d\n", fromBlock)

	for {
		select {
		case err = <-sub.Err():
			return err
		case <-ctx.Done():
			return nil
		case vLog := <-logChan:
			log.Printf("Received log: %v\n", vLog)
			err := w.processLogs(ctx, client, vLog)
			if err != nil {
				return err
			}
		}
	}
}

func (w *Watcher) processPastEvents(ctx context.Context, client *ethclient.Client, fromBlock uint64) error {
	query := ethereum.FilterQuery{
		Addresses: w.contractAddresses,
		Topics: [][]common.Hash{
			{addedBlackListSig, removedBlackListSig, destroyedBlackFundsSig},
		},
		FromBlock: big.NewInt(int64(fromBlock)),
	}
	logs, err := client.FilterLogs(ctx, query)
	if err != nil {
		return err
	}
	for _, vLog := range logs {
		err := w.processLogs(ctx, client, vLog)
		if err != nil {
			return err
		}
	}
	log.Printf("Processed %d past events\n", len(logs))
	return nil
}

func (w *Watcher) processLogs(ctx context.Context, client *ethclient.Client, vLog types.Log) error {
	if len(vLog.Topics) == 0 {
		return nil
	}

	block, err := client.BlockByNumber(ctx, big.NewInt(int64(vLog.BlockNumber)))
	if err != nil {
		panic(err)
	}
	blockDate := time.Unix(int64(block.Time()), 0)

	topic := vLog.Topics[0]

	contractAddress := strings.ToLower(vLog.Address.String())
	contract, found := blwatcher.AddressContractMap[contractAddress]
	if !found {
		// Should not be possible
		return fmt.Errorf("unknown contract address %s", contractAddress)
	}

	if topic == addedBlackListSig {
		event, err := w.contractAbiMap[vLog.Address].Unpack("AddedBlackList", vLog.Data)
		if err != nil {
			return err
		}

		blacklistedAddress := event[0].(common.Address)
		w.eventChan <- &blwatcher.Event{
			Date:        blockDate,
			Contract:    contract,
			Address:     blacklistedAddress.String(),
			Tx:          vLog.TxHash.String(),
			BlockNumber: vLog.BlockNumber,
			Type:        blwatcher.AddBlacklistEvent,
			Amount:      0,
		}
		return nil
	} else if topic == removedBlackListSig {
		event, err := w.contractAbiMap[vLog.Address].Unpack("RemovedBlackList", vLog.Data)
		if err != nil {
			return err
		}
		clearedAddress := event[0].(common.Address)
		w.eventChan <- &blwatcher.Event{
			Date:        blockDate,
			Contract:    contract,
			Address:     clearedAddress.String(),
			Tx:          vLog.TxHash.String(),
			BlockNumber: vLog.BlockNumber,
			Type:        blwatcher.RemoveBlacklistEvent,
			Amount:      0,
		}
		return nil
	} else if topic == destroyedBlackFundsSig {
		event, err := w.contractAbiMap[vLog.Address].Unpack("DestroyedBlackFunds", vLog.Data)
		if err != nil {
			return err
		}
		blacklistedAddress := event[0].(common.Address)
		amount := event[1].(*big.Int).Int64()
		w.eventChan <- &blwatcher.Event{
			Date:        blockDate,
			Contract:    contract,
			Address:     blacklistedAddress.String(),
			Tx:          vLog.TxHash.String(),
			BlockNumber: vLog.BlockNumber,
			Type:        blwatcher.DestroyBlackFundsEvent,
			Amount:      amount,
		}
		return nil
	}
	return fmt.Errorf("unknown topic %s", topic.String())
}
