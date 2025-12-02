package internal

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"blwatcher"
)

var addedBlackListSig = crypto.Keccak256Hash([]byte("AddedBlackList(address)"))                   // USDT
var removedBlackListSig = crypto.Keccak256Hash([]byte("RemovedBlackList(address)"))               // USDT
var destroyedBlackFundsSig = crypto.Keccak256Hash([]byte("DestroyedBlackFunds(address,uint256)")) // USDT
var blacklistedSig = crypto.Keccak256Hash([]byte("Blacklisted(address)"))                         // USDC
var unblacklistedSig = crypto.Keccak256Hash([]byte("UnBlacklisted(address)"))                     // USDC
var submissionSig = crypto.Keccak256Hash([]byte("Submission(uint256)"))                           // USDT multisig

type Topic struct {
	eventType blwatcher.EventType
	funcName  string
}

var topics = map[common.Hash]Topic{
	addedBlackListSig:      {blwatcher.AddBlacklistEvent, "AddedBlackList"},
	removedBlackListSig:    {blwatcher.RemoveBlacklistEvent, "RemovedBlackList"},
	destroyedBlackFundsSig: {blwatcher.DestroyBlackFundsEvent, "DestroyedBlackFunds"},
	blacklistedSig:         {blwatcher.AddBlacklistEvent, "Blacklisted"},
	unblacklistedSig:       {blwatcher.RemoveBlacklistEvent, "UnBlacklisted"},
	submissionSig:          {blwatcher.SubmitBlacklistEvent, "Submission"},
}

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
			{addedBlackListSig, removedBlackListSig, destroyedBlackFundsSig, blacklistedSig, unblacklistedSig, submissionSig},
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
		err := w.processPastEvents(ctx, client, query)
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

func (w *Watcher) processPastEvents(ctx context.Context, client *ethclient.Client, query ethereum.FilterQuery) error {
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

	if vLog.Removed {
		return nil
	}

	block, err := client.BlockByNumber(ctx, big.NewInt(int64(vLog.BlockNumber)))
	if err != nil {
		panic(err)
	}
	blockDate := time.Unix(int64(block.Time()), 0)

	topic := vLog.Topics[0]
	if topic == submissionSig {
		return w.handleSubmission(ctx, client, vLog, blockDate)
	}

	contractAddress := strings.ToLower(vLog.Address.String())
	contract, found := blwatcher.AddressContractMap[contractAddress]
	if !found {
		// Should not be possible
		return fmt.Errorf("unknown contract address %s", contractAddress)
	}

	t, found := topics[topic]
	if !found {
		return fmt.Errorf("unknown topic %s", topic.String())
	}
	var address common.Address
	event, err := w.contractAbiMap[vLog.Address].Unpack(t.funcName, vLog.Data)
	if err != nil {
		return err
	}
	if len(vLog.Data) != 0 {
		// Address is in the data
		address = event[0].(common.Address)
	} else {
		// Address is in the topic
		address = common.HexToAddress(vLog.Topics[1].String())
	}
	var amount int64
	if t.eventType == blwatcher.DestroyBlackFundsEvent {
		amount = event[1].(*big.Int).Int64()
	}
	w.eventChan <- &blwatcher.Event{
		Date:        blockDate,
		Contract:    contract,
		Address:     address.String(),
		Tx:          vLog.TxHash.String(),
		BlockNumber: vLog.BlockNumber,
		Type:        t.eventType,
		Amount:      amount,
	}
	return nil
}

func (w *Watcher) handleSubmission(ctx context.Context, client *ethclient.Client, vLog types.Log, blockDate time.Time) error {
	if len(vLog.Topics) < 2 {
		return fmt.Errorf("submission log missing transaction id")
	}

	txID := vLog.Topics[1].Big()
	destination, data, err := w.getMultisigTransaction(ctx, client, vLog.Address, txID, vLog.BlockNumber)
	if err != nil {
		return err
	}

	if strings.ToLower(destination.Hex()) != blwatcher.USDTContractAddress {
		return nil
	}

	target, ok, err := w.decodeUSDTBlacklistData(data)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	contract := blwatcher.AddressContractMap[blwatcher.USDTContractAddress]
	w.eventChan <- &blwatcher.Event{
		Date:        blockDate,
		Contract:    contract,
		Address:     target.String(),
		Tx:          vLog.TxHash.String(),
		BlockNumber: vLog.BlockNumber,
		Type:        blwatcher.SubmitBlacklistEvent,
	}

	return nil
}

func (w *Watcher) getMultisigTransaction(ctx context.Context, client *ethclient.Client, contract common.Address, txID *big.Int, blockNumber uint64) (common.Address, []byte, error) {
	input, err := w.contractAbiMap[contract].Pack("transactions", txID)
	if err != nil {
		return common.Address{}, nil, err
	}

	blockNum := new(big.Int).SetUint64(blockNumber)
	result, err := client.CallContract(ctx, ethereum.CallMsg{To: &contract, Data: input}, blockNum)
	if err != nil {
		return common.Address{}, nil, err
	}

	output, err := w.contractAbiMap[contract].Unpack("transactions", result)
	if err != nil {
		return common.Address{}, nil, err
	}
	if len(output) < 3 {
		return common.Address{}, nil, fmt.Errorf("unexpected transactions output length")
	}

	destination := output[0].(common.Address)
	dataBytes, ok := output[2].([]byte)
	if !ok {
		return common.Address{}, nil, fmt.Errorf("unexpected data type for multisig tx data")
	}

	return destination, dataBytes, nil
}

func (w *Watcher) decodeUSDTBlacklistData(data []byte) (common.Address, bool, error) {
	if len(data) < 4 {
		return common.Address{}, false, nil
	}

	usdtABI := w.contractAbiMap[common.HexToAddress(blwatcher.USDTContractAddress)]
	method, ok := usdtABI.Methods["addBlackList"]
	if !ok {
		return common.Address{}, false, fmt.Errorf("addBlackList method missing in ABI")
	}
	if !bytes.Equal(data[:4], method.ID) {
		return common.Address{}, false, nil
	}

	args, err := method.Inputs.Unpack(data[4:])
	if err != nil {
		return common.Address{}, false, err
	}
	if len(args) == 0 {
		return common.Address{}, false, nil
	}

	address, ok := args[0].(common.Address)
	if !ok {
		return common.Address{}, false, fmt.Errorf("unexpected type for addBlackList argument")
	}

	return address, true, nil
}
