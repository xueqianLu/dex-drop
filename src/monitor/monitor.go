package monitor

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/hpdex-project/dex-drop/src/config"
	log "github.com/sirupsen/logrus"
	"math/big"
	"strings"
	"sync"
	"time"
)

type PairInfo struct {
	token0 common.Address
	token1 common.Address
}
type TxInfo struct {
	token0 *big.Int
	token1 *big.Int
}

type TraceService struct {
	HandledTx    map[string]bool // 已经处理的交易哈希
	HandledBlock map[string]bool //

	routerAddr common.Address
	tokenPairs []*PairInfo

	tool *EthTool

	orderCh chan *types.Block
	rwMux   sync.RWMutex
	wg      sync.WaitGroup

	closed chan struct{}

	blockTxs map[string][]*types.Transaction // blockhash ==> txs
	txBooks  map[string]*TxInfo              // txhash	  ==> listOrderBooks
}

func NewTraceService(pairs []*PairInfo) *TraceService {
	t := &TraceService{
		HandledTx:    make(map[string]bool),
		HandledBlock: make(map[string]bool),
		orderCh:      make(chan *types.Block, 100),
		routerAddr:   common.HexToAddress(config.GetConfig().OrdersBookContract),
		tokenPairs:   pairs,
		closed:       make(chan struct{}, 1),
		txBooks:      make(map[string]*TxInfo),
		blockTxs:     make(map[string][]*types.Transaction),
	}
	tool, err := NewEthTool(config.GetConfig().Web3Provider, config.GetConfig().OrdersBookContract)
	if err == nil {
		t.tool = tool
	} else {
		log.WithFields(log.Fields{
			"err": err,
		}).Error("NewTradingCamp  newEthTool error")
	}

	return t
}

func (t *TraceService) Start() {
	// 1. load db.
	t.LoadData()
	// 2. start monitor
	t.wg.Add(2)
	//go t.monitorNewBlock()
	go t.loopNewBlock()
	go t.task()
	// 3. stop and update db.
	// 4. query.
}

func (t *TraceService) Stop() {
	close(t.closed)
	t.wg.Wait()
	log.Debug("TraceService stopped.")
}

type userinfo struct {
	tokenPair common.Address
	token     common.Address
	userAddr  common.Address
	cell      *big.Int
	buy       *big.Int
}

type ParsedResult struct {
	inStatics bool
	lastIndex int
	token     common.Address
	//dbpi      *db.PairInfo
	taker common.Address
	maker map[string]bool
	isBuy bool
	fee   *big.Int
}

// 统计所有 HPB/USDT 交易对

// hpb->usdt : 调用 router 合约的 swapExactETHForTokens 方法.
// txhash:0x8a19f5aa0e413c27bafc06afde439e5cd5056a68924bbee523e0cc98f78e1a11,
// txdata:0x7ff36ab500000000000000000000000000000000000000000000000003b78c7fb5fc2e50000000000000000000000000000000000000000000000000000000000000008000000000000000000000000091ea38762a2ff06a3d32ec8d05a497a3555d3ab8000000000000000000000000000000000000000000000000000000006142251d0000000000000000000000000000000000000000000000000000000000000002000000000000000000000000be05ac1fb417c9ea435b37a9cecd39bc70359d31000000000000000000000000e78984541a634c52c760fbf97ca3f8e7d8f04c85
// usdt->hpb : 调用 router 合约的 swapExactTokensForETH 方法.
// txhash:0x4134210af73a3397e1163762ab307e641e7b96ecc7dec6da9ae08e8597517474
// txdata:0x18cbafe500000000000000000000000000000000000000000000000003bc4e7b3abc296d0000000000000000000000000000000000000000000000000db9dc3cb65d511c00000000000000000000000000000000000000000000000000000000000000a000000000000000000000000091ea38762a2ff06a3d32ec8d05a497a3555d3ab8000000000000000000000000000000000000000000000000000000006142260a0000000000000000000000000000000000000000000000000000000000000002000000000000000000000000e78984541a634c52c760fbf97ca3f8e7d8f04c85000000000000000000000000be05ac1fb417c9ea435b37a9cecd39bc70359d31

// 统计 WHPB/USDT 交易对
// whpb->usdt : 调用 router 合约的 swapExactTokensForTokens 方法.
// txhash:0x3bd6ca9cbc727479b8e5d9a7583ac8711b6ec43b0f53bd8f6bc7e1bda6cdc617
// txdata:0x38ed17390000000000000000000000000000000000000000000000000de0b6b3a764000000000000000000000000000000000000000000000000000003ced18fbbb0d14800000000000000000000000000000000000000000000000000000000000000a000000000000000000000000091ea38762a2ff06a3d32ec8d05a497a3555d3ab800000000000000000000000000000000000000000000000000000000614226cd0000000000000000000000000000000000000000000000000000000000000002000000000000000000000000be05ac1fb417c9ea435b37a9cecd39bc70359d31000000000000000000000000e78984541a634c52c760fbf97ca3f8e7d8f04c85
// usdt->whpb : 调用 router 合约的 swapExactTokensForTokens 方法.
// txhash:0xa89d37544e45b8c425842a775148fb3bd0f08eb5009cd498c9e6d5ec9a0aa02f
// txdata:0x38ed173900000000000000000000000000000000000000000000000003d3b1544ab58aa60000000000000000000000000000000000000000000000000db9c85247d5126700000000000000000000000000000000000000000000000000000000000000a000000000000000000000000091ea38762a2ff06a3d32ec8d05a497a3555d3ab800000000000000000000000000000000000000000000000000000000614227620000000000000000000000000000000000000000000000000000000000000002000000000000000000000000e78984541a634c52c760fbf97ca3f8e7d8f04c85000000000000000000000000be05ac1fb417c9ea435b37a9cecd39bc70359d31
func (t *TraceService) task() {
	defer t.wg.Done()

	for {
		select {
		case <-t.closed:
			return

		case newBlock, ok := <-t.orderCh:
			if !ok {
				return
			}
			block := newBlock
			t.dealBlock(block)
		}
	}
}

func (t *TraceService) loopNewBlock() {
	defer t.wg.Done()
	defer log.Debug("TraceService monitorNewBlock task exit")

	log.Debug("TraceService monitorNewBlock task started")

	e := t.tool
	var blockNumber uint64 = 0

	//load contract ABI
	tokenAbi, err := abi.JSON(strings.NewReader(hpbswap.Erc20ABI))
	if err != nil {
		log.WithFields(log.Fields{
			"err": err,
		}).Error("Erc20ABI error")
		return
	}

	logTransferSig := []byte("Transfer(address,address,uint256)")
	LogApprovalSig := []byte("Approval(address,address,uint256)")
	logTransferSigHash := crypto.Keccak256Hash(logTransferSig)
	logApprovalSigHash := crypto.Keccak256Hash(LogApprovalSig)

	orderBookAddress := common.HexToAddress(e.orderBookAddr)

	for {
		select {
		case <-t.closed:
			return

		default:
			current, err := e.client.BlockNumber(e.ctx)
			if err != nil {
				log.Error("get blockNumber failed", "err", err)
				time.Sleep(time.Second)
				continue
			}
			if blockNumber == 0 {
				blockNumber = current
			}

			if blockNumber <= current {
				log.Debug("got new header number ", blockNumber)
				block, err := e.client.BlockByNumber(e.ctx, big.NewInt(int64(blockNumber)))
				if err != nil {
					log.Error("get block by number err ", err)
					continue
				}
				fmt.Println("get block by number ", blockNumber, "has tx ", len(block.Transactions()))
				if _, exist := t.HandledBlock[block.Hash().Hex()]; exist {
					continue
				} else {
					t.HandledBlock[block.Hash().Hex()] = true
				}
				needCommit := false

				for _, tx := range block.Transactions() {
					if tx.To() != nil && equalAddress(*tx.To(), orderBookAddress) {
						// 判断交易是否已经处理过.
						if _, exist := t.HandledTx[tx.Hash().Hex()]; exist {
							continue
						} else {
							t.HandledTx[tx.Hash().Hex()] = true
						}

						fmt.Println("got order book transaction")
						receipt, err := e.client.TransactionReceipt(e.ctx, tx.Hash())
						if err != nil {
							log.Debug("can't get tx receipt", "err", err)
							continue
						}

						ordersBook := make([]OrdersBook, 0)
						index := 0

						for _, vLog := range receipt.Logs {
							if bytes.Compare(logTransferSigHash.Bytes(), vLog.Topics[0].Bytes()) == 0 {
								index++
								var transferEvent LogTransfer

								err := tokenAbi.UnpackIntoInterface(&transferEvent, "Transfer", vLog.Data)
								if err != nil {
									break
								}

								transferEvent.From = common.HexToAddress(vLog.Topics[1].Hex())
								transferEvent.To = common.HexToAddress(vLog.Topics[2].Hex())

								ordersBook = append(ordersBook, OrdersBook{LogTransfer{
									From:   common.HexToAddress(vLog.Topics[1].Hex()),
									To:     common.HexToAddress(vLog.Topics[2].Hex()),
									Tokens: transferEvent.Tokens,
								},
									vLog.Address,
									receipt.BlockNumber,
									index,
									block.Time(),
								})
							} else if bytes.Compare(logApprovalSigHash.Bytes(), vLog.Topics[0].Bytes()) == 0 {
								var approvalEvent LogApproval

								err := tokenAbi.UnpackIntoInterface(&approvalEvent, "Approval", vLog.Data)
								if err != nil {
									break
								}
								approvalEvent.TokenOwner = common.HexToAddress(vLog.Topics[1].Hex())
								approvalEvent.Spender = common.HexToAddress(vLog.Topics[2].Hex())
							}
						}
						if len(ordersBook) > 0 {
							t.rwMux.Lock()
							if txs, exist := t.blockTxs[block.Hash().Hex()]; !exist {
								txs = make([]*types.Transaction, 0)
								txs = append(txs, tx)
								t.blockTxs[block.Hash().Hex()] = txs
							} else {
								txs = append(txs, tx)
								t.blockTxs[block.Hash().Hex()] = txs
							}
							t.txBooks[tx.Hash().Hex()] = ordersBook
							needCommit = true
							t.rwMux.Unlock()
						}
					}

				}
				if needCommit {
					log.Debug("send block to order channel")
					t.orderCh <- block
				}
				if (blockNumber - current) == 0 {
					//
					time.Sleep(time.Second)
				}
				blockNumber += 1
			} else {
				time.Sleep(time.Second)
			}
		}
	}
}

func equalAddress(a, b common.Address) bool {
	return bytes.Compare(a.Bytes(), b.Bytes()) == 0
}

func equalAddrString(a, b string) bool {
	addrA := common.HexToAddress(a)
	addrB := common.HexToAddress(b)
	return equalAddress(addrA, addrB)
}
