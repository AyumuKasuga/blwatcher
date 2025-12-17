package internal

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"blwatcher"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
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

type evmWatcher struct {
	blockchain        blwatcher.Blockchain
	contractAddresses []common.Address
	nodeURL           string
	contractAbiMap    map[common.Address]abi.ABI
	contractMap       map[string]blwatcher.Contract
	eventChan         chan *blwatcher.Event
	eventStorage      blwatcher.EventStorage
}

func newEVMWatcher(
	blockchain blwatcher.Blockchain,
	contracts []blwatcher.Contract,
	rpcURL string,
	eventChan chan *blwatcher.Event,
	eventStorage blwatcher.EventStorage,
) blwatcher.Watcher {
	contractAddresses := make([]common.Address, len(contracts))
	contractAbiMap := make(map[common.Address]abi.ABI, len(contracts))
	contractMap := make(map[string]blwatcher.Contract, len(contracts))
	for i, contract := range contracts {
		if contract.Blockchain == "" {
			contract.Blockchain = blockchain
		}
		contractAddresses[i] = common.HexToAddress(contract.Address)
		contractAbi, err := abi.JSON(strings.NewReader(contract.AbiJSON))
		if err != nil {
			panic(err)
		}
		contractAbiMap[contractAddresses[i]] = contractAbi
		contractMap[strings.ToLower(contract.Address)] = contract
	}
	return &evmWatcher{
		blockchain:        blockchain,
		contractAddresses: contractAddresses,
		nodeURL:           rpcURL,
		contractAbiMap:    contractAbiMap,
		contractMap:       contractMap,
		eventChan:         eventChan,
		eventStorage:      eventStorage,
	}
}

func NewWatcher(
	contracts []blwatcher.Contract,
	ethNodeURL string,
	eventChan chan *blwatcher.Event,
	eventStorage blwatcher.EventStorage,
) blwatcher.Watcher {
	return newEVMWatcher(blwatcher.BlockchainEthereum, contracts, ethNodeURL, eventChan, eventStorage)
}

func NewArbitrumWatcher(
	contracts []blwatcher.Contract,
	rpcURL string,
	eventChan chan *blwatcher.Event,
	eventStorage blwatcher.EventStorage,
) blwatcher.Watcher {
	return newEVMWatcher(blwatcher.BlockchainArbitrum, contracts, rpcURL, eventChan, eventStorage)
}

func NewBaseWatcher(
	contracts []blwatcher.Contract,
	rpcURL string,
	eventChan chan *blwatcher.Event,
	eventStorage blwatcher.EventStorage,
) blwatcher.Watcher {
	return newEVMWatcher(blwatcher.BlockchainBase, contracts, rpcURL, eventChan, eventStorage)
}

func NewOptimismWatcher(
	contracts []blwatcher.Contract,
	rpcURL string,
	eventChan chan *blwatcher.Event,
	eventStorage blwatcher.EventStorage,
) blwatcher.Watcher {
	return newEVMWatcher(blwatcher.BlockchainOptimism, contracts, rpcURL, eventChan, eventStorage)
}

func NewAvalancheWatcher(
	contracts []blwatcher.Contract,
	rpcURL string,
	eventChan chan *blwatcher.Event,
	eventStorage blwatcher.EventStorage,
) blwatcher.Watcher {
	return newEVMWatcher(blwatcher.BlockchainAvalanche, contracts, rpcURL, eventChan, eventStorage)
}

func NewPolygonWatcher(
	contracts []blwatcher.Contract,
	rpcURL string,
	eventChan chan *blwatcher.Event,
	eventStorage blwatcher.EventStorage,
) blwatcher.Watcher {
	return newEVMWatcher(blwatcher.BlockchainPolygon, contracts, rpcURL, eventChan, eventStorage)
}

func NewZkSyncWatcher(
	contracts []blwatcher.Contract,
	rpcURL string,
	eventChan chan *blwatcher.Event,
	eventStorage blwatcher.EventStorage,
) blwatcher.Watcher {
	return newEVMWatcher(blwatcher.BlockchainZkSync, contracts, rpcURL, eventChan, eventStorage)
}

func NewLineaWatcher(
	contracts []blwatcher.Contract,
	rpcURL string,
	eventChan chan *blwatcher.Event,
	eventStorage blwatcher.EventStorage,
) blwatcher.Watcher {
	return newEVMWatcher(blwatcher.BlockchainLinea, contracts, rpcURL, eventChan, eventStorage)
}

func (w *evmWatcher) Name() string {
	return string(w.blockchain)
}

func (w *evmWatcher) prefix() string {
	name := w.Name()
	if name == "" {
		return "[?]"
	}
	return "[" + strings.ToUpper(name) + "]"
}

func (w *evmWatcher) errorf(format string, args ...interface{}) error {
	return fmt.Errorf("%s "+format, append([]interface{}{w.prefix()}, args...)...)
}

func (w *evmWatcher) Watch(ctx context.Context) error {
	client, err := ethclient.Dial(w.nodeURL)
	if err != nil {
		return err
	}
	fromBlock, err := w.eventStorage.GetLastEventBlock(w.blockchain)
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

	watchErr := w.processPastEvents(ctx, client, query)
	if watchErr != nil {
		return watchErr
	}

	log.Printf("%s Start watching from block %d\n", w.prefix(), fromBlock)

	for {
		select {
		case err = <-sub.Err():
			return err
		case <-ctx.Done():
			return nil
		case vLog := <-logChan:
			log.Printf("%s Received log: %v\n", w.prefix(), vLog)
			err := w.processLogs(ctx, client, vLog)
			if err != nil {
				return err
			}
		}
	}
}

func (w *evmWatcher) processPastEvents(ctx context.Context, client *ethclient.Client, query ethereum.FilterQuery) error {
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
	log.Printf("%s Processed %d past events\n", w.prefix(), len(logs))
	return nil
}

func (w *evmWatcher) processLogs(ctx context.Context, client *ethclient.Client, vLog types.Log) error {
	if len(vLog.Topics) == 0 {
		return nil
	}

	if vLog.Removed {
		return nil
	}

	header, err := client.HeaderByNumber(ctx, big.NewInt(int64(vLog.BlockNumber)))
	if err != nil {
		return w.errorf("failed to fetch block header for %d: %w", vLog.BlockNumber, err)
	}
	blockDate := time.Unix(int64(header.Time), 0)

	topic := vLog.Topics[0]
	if topic == submissionSig {
		return w.handleSubmission(ctx, client, vLog, blockDate)
	}

	contractAddress := strings.ToLower(vLog.Address.String())
	contract, found := w.contractMap[contractAddress]
	if !found {
		// Should not be possible
		return w.errorf("unknown contract address %s", contractAddress)
	}

	t, found := topics[topic]
	if !found {
		return w.errorf("unknown topic %s", topic.String())
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
		Blockchain:  w.blockchain,
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

func (w *evmWatcher) handleSubmission(ctx context.Context, client *ethclient.Client, vLog types.Log, blockDate time.Time) error {
	if w.blockchain != blwatcher.BlockchainEthereum {
		return nil
	}

	if len(vLog.Topics) < 2 {
		return w.errorf("submission log missing transaction id")
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

	contract := w.contractMap[strings.ToLower(blwatcher.USDTContractAddress)]
	w.eventChan <- &blwatcher.Event{
		Blockchain:  w.blockchain,
		Date:        blockDate,
		Contract:    contract,
		Address:     target.String(),
		Tx:          vLog.TxHash.String(),
		BlockNumber: vLog.BlockNumber,
		Type:        blwatcher.SubmitBlacklistEvent,
	}

	return nil
}

func (w *evmWatcher) getMultisigTransaction(ctx context.Context, client *ethclient.Client, contract common.Address, txID *big.Int, blockNumber uint64) (common.Address, []byte, error) {
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
		return common.Address{}, nil, w.errorf("unexpected transactions output length")
	}

	destination := output[0].(common.Address)
	dataBytes, ok := output[2].([]byte)
	if !ok {
		return common.Address{}, nil, w.errorf("unexpected data type for multisig tx data")
	}

	return destination, dataBytes, nil
}

func (w *evmWatcher) decodeUSDTBlacklistData(data []byte) (common.Address, bool, error) {
	if len(data) < 4 {
		return common.Address{}, false, nil
	}

	usdtABI := w.contractAbiMap[common.HexToAddress(blwatcher.USDTContractAddress)]
	method, ok := usdtABI.Methods["addBlackList"]
	if !ok {
		return common.Address{}, false, w.errorf("addBlackList method missing in ABI")
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
		return common.Address{}, false, w.errorf("unexpected type for addBlackList argument")
	}

	return address, true, nil
}
