package src

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
	"github.com/ethereum/go-ethereum/log"
	"math/big"
	"strings"
	"sync"
	"time"
)
type TxInfo struct {
	token0	*big.Int
	token1  *big.Int
}

type TraceService struct {
	HandledTx    map[string]bool // 已经处理的交易哈希
	HandledBlock map[string]bool //

	dbLoadFinish  bool
	orderBookAddr common.Address

	orderCh chan *types.Block
	rwMux   sync.RWMutex
	wg      sync.WaitGroup

	tool *EthTool

	closed            chan struct{}

	blockTxs map[string][]*types.Transaction // blockhash ==> txs
	txBooks  map[string]ListOrdersBook       // txhash	  ==> listOrderBooks

	saveCh chan model.CompetitionRecords // used to save records into myqsl.
}

func topToken(pair *db.PairInfo) common.Address {
	if pair.BaseTokenIndex == 0 {
		return common.HexToAddress(pair.Token1)
	}
	return common.HexToAddress(pair.Token0)
}
func baseToken(pair *db.PairInfo) common.Address {
	if pair.BaseTokenIndex == 0 {
		return common.HexToAddress(pair.Token0)
	}
	return common.HexToAddress(pair.Token1)
}

func NewTradingCampService(pair []*db.PairInfo) *TradingCampService {
	t := &TradingCampService{
		HandledTx:         make(map[string]bool),
		HandledBlock:      make(map[string]bool),
		orderCh:           make(chan *types.Block, 100),
		orderBookAddr:     common.HexToAddress(config.GetConfig().OrdersBookContract),
		supportTokenPairs: pair,
		dbLoadFinish:      false,
		closed:            make(chan struct{}, 1),
		txBooks:           make(map[string]ListOrdersBook),
		blockTxs:          make(map[string][]*types.Transaction),
		saveCh:            make(chan model.CompetitionRecords, 100), // used to save the record to mysql.
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

func (t *TradingCampService) Start() {
	// 1. load db.
	t.LoadData()
	// 2. start monitor
	t.wg.Add(3)
	//go t.monitorNewBlock()
	go t.loopNewBlock()
	go t.task()
	go t.saveToDB()
	// 3. stop and update db.
	// 4. query.
}

func (t *TradingCampService) saveToDB() {
	defer t.wg.Done()
	defer log.Debug("tradingCampService saveToDB task exit")

	log.Debug("tradingCampService saveToDB task started")
	for {
		select {
		case <-t.closed:
			// 退出前将未入库的数据入库.
			if len(t.saveCh) > 0 {
				for records := range t.saveCh {
					for _, r := range records {
						_, err := r.Save()
						if err != nil {
							log.Error("save record to db failed, err ", err)
							continue
						}
					}
					if len(t.saveCh) == 0 {
						break
					}
				}
			}
			return
		case records := <-t.saveCh:
			for _, r := range records {
				_, err := r.Save()
				if err != nil {
					log.Error("save record to db failed, err ", err)
					continue
				}
			}
		}
	}
}

func (t *TradingCampService) Stop() {
	close(t.closed)
	t.wg.Wait()
	log.Debug("tradingCampService stopped.")
}

func (t *TradingCampService) LoadData() {
	gorm := db.GetORM()
	if gorm.HasTable(&db.Compation{}) {
		gorm.AutoMigrate(&db.Compation{})
	} else {
		gorm.CreateTable(&db.Compation{})
	}

	t.dbLoadFinish = true
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
	dbpi      *db.PairInfo
	taker     common.Address
	maker     map[string]bool
	isBuy     bool
	fee       *big.Int
}

func (t *TradingCampService) parseOrdersBook(ordersBook []OrdersBook) (result ParsedResult) {

	countLog := len(ordersBook)
	if countLog <= 1 {
		result.inStatics = false
		return
	}
	result.fee = big.NewInt(0)
	result.maker = make(map[string]bool)
	result.taker = ordersBook[0].From

	cellToken := ordersBook[0].TokenAddress
	lastIndex := countLog - 1

	//最后一笔是否是收款账号收取手续费
	if equalAddress(ordersBook[countLog-1].To, common.HexToAddress(config.GetConfig().FeeOwner)) &&
		equalAddress(ordersBook[countLog-1].TokenAddress, ordersBook[0].TokenAddress) {
		result.fee = ordersBook[countLog-1].Tokens
		lastIndex = countLog - 2
	}
	receiveToken := ordersBook[lastIndex].TokenAddress

	result.lastIndex = lastIndex

	// 获取交易对信息
	result.dbpi = &db.PairInfo{}
	if err := result.dbpi.GetByToken(cellToken.String(), receiveToken.String()); err != nil {
		log.WithFields(log.Fields{
			"err": err,
		}).Error("tradingCamp get pairInfo by token error")
		return
	}

	// 判断是否需要进行统计
	for _, p := range t.supportTokenPairs {
		if strings.Compare(result.dbpi.PairAddress, p.PairAddress) == 0 {
			result.inStatics = true
			result.token = topToken(p)
			break
		}
	}

	if !result.inStatics {
		return
	}

	if equalAddress(result.token, receiveToken) {
		result.isBuy = true
	}

	for i, lval := range ordersBook {
		if i == 0 || i >= result.lastIndex {
			continue
		}
		if equalAddress(lval.To, t.orderBookAddr) == false &&
			equalAddress(lval.To, common.HexToAddress(result.dbpi.PairAddress)) == false {
			result.maker[lval.To.Hex()] = true
		}
	}
	return
}

// taker is cell coin.
func (t *TradingCampService) calcAmount1(parsed ParsedResult, oderBooks []OrdersBook) map[string]*userinfo {
	cache := make(map[string]*userinfo)
	// taker 卖出 顶对币种，统计 每个 Maker 买入的数量.
	for i, lval := range oderBooks {
		if i == 0 {
			// taker 卖出代币
			// 统计taker 卖出总量
			cell := lval.Tokens
			usr := &userinfo{
				tokenPair: common.HexToAddress(parsed.dbpi.PairAddress),
				userAddr:  parsed.taker,
				token:     parsed.token,
				cell:      new(big.Int).Set(cell),
				buy:       big.NewInt(0),
			}
			cache[parsed.taker.Hex()] = usr
		}

		if i == 0 || i >= parsed.lastIndex {
			continue
		}
		if !equalAddress(lval.To, t.orderBookAddr) &&
			!equalAddress(lval.To, common.HexToAddress(parsed.dbpi.PairAddress)) {
			// this is a maker.
			maker := lval.To
			makerBuy := lval.Tokens
			if equalAddress(maker, parsed.taker) {
				// 卖给了自己的买单
				usr := cache[parsed.taker.Hex()]
				usr.cell = new(big.Int).Sub(usr.cell, makerBuy)
				cache[parsed.taker.Hex()] = usr
			} else {
				if info, exist := cache[maker.Hex()]; !exist {
					usr := &userinfo{
						tokenPair: common.HexToAddress(parsed.dbpi.PairAddress),
						userAddr:  maker,
						token:     parsed.token,
						cell:      big.NewInt(0),
						buy:       new(big.Int).Set(makerBuy),
					}
					cache[maker.Hex()] = usr
				} else {
					info.buy = new(big.Int).Add(info.buy, makerBuy)
					cache[maker.Hex()] = info
				}
			}
		}
	}
	return cache
}

// taker is buy coin and maker number <= 1.
func (t *TradingCampService) calcAmount2(parsed ParsedResult, oderBooks []OrdersBook) map[string]*userinfo {
	cache := make(map[string]*userinfo)

	// taker 买入 顶对币种，统计 maker 卖出的数量.
	buy := big.NewInt(0)
	uniswap := big.NewInt(0)
	for i, lval := range oderBooks {
		if i == 0 || i > parsed.lastIndex {
			continue
		}
		if equalAddress(lval.From, common.HexToAddress(parsed.dbpi.PairAddress)) &&
			equalAddress(lval.To, t.orderBookAddr) {
			// uniswap token
			uniswap = lval.Tokens
		}

		if i == parsed.lastIndex {
			buy = lval.Tokens
			makerCell := big.NewInt(0)
			if len(parsed.maker) == 1 {
				makerCell = new(big.Int).Sub(buy, uniswap)
				var maker common.Address
				for k := range parsed.maker {
					maker = common.HexToAddress(k)
					break
				}
				if equalAddress(maker, parsed.taker) {
					// 买到了自己的卖单
					buy = new(big.Int).Sub(buy, makerCell)

				} else {
					if info, exist := cache[maker.Hex()]; !exist {
						usr := &userinfo{
							tokenPair: common.HexToAddress(parsed.dbpi.PairAddress),
							userAddr:  maker,
							token:     parsed.token,
							cell:      new(big.Int).Set(makerCell),
							buy:       big.NewInt(0),
						}
						cache[maker.Hex()] = usr
					} else {
						info.cell = makerCell
						cache[maker.Hex()] = info
					}
				}
			}
			// taker 买入
			usr := &userinfo{
				tokenPair: common.HexToAddress(parsed.dbpi.PairAddress),
				userAddr:  parsed.taker,
				token:     parsed.token,
				cell:      big.NewInt(0),
				buy:       new(big.Int).Set(buy),
			}
			cache[parsed.taker.Hex()] = usr
		}
	}
	return cache
}

type group struct {
	user   map[string]bool
	piinfo *db.PairInfo
}

func (t *TradingCampService) calcAmount3(groups map[string]*group, block *types.Block) map[string]map[string]*userinfo {
	allCache := make(map[string]map[string]*userinfo)
	for _, gr := range groups {
		cache := make(map[string]*userinfo)
		allCache[gr.piinfo.PairAddress] = cache

		from := topToken(gr.piinfo)
		to := baseToken(gr.piinfo)

		for userAddr := range gr.user {
			user := common.HexToAddress(userAddr)
			cell, buy, err := t.tool.GetVolumeByUserBetweenBlock(big.NewInt(int64(block.NumberU64())-1),
				big.NewInt(int64(block.NumberU64())), user, from, to)

			if err != nil {
				log.WithFields(log.Fields{
					"err": err,
				}).Error("getVolumeByUserBetweenBlock error")
				continue
			}
			usr := &userinfo{
				tokenPair: common.HexToAddress(gr.piinfo.PairAddress),
				userAddr:  user,
				token:     from,
				cell:      new(big.Int).Set(cell),
				buy:       new(big.Int).Set(buy),
			}
			cache[user.Hex()] = usr
		}
	}
	return allCache
}

func (t *TradingCampService) dealBlock(block *types.Block) {
	t.rwMux.Lock()
	defer t.rwMux.Unlock()

	txs, exist := t.blockTxs[block.Hash().Hex()]
	if !exist {
		return
	}
	records := make([]*model.CompetitionRecord, 0)
	allCache := make(map[string]map[string]*userinfo)

	if len(txs) == 1 {
		// 块内只有一笔订单簿交易
		tx := txs[0]
		oderBooks := t.txBooks[tx.Hash().Hex()]
		log.Debug("block has one tx")

		// parse all taker and maker.
		parsed := t.parseOrdersBook(oderBooks)
		log.Debug("after parse", "parsed ", parsed)
		if !parsed.inStatics {
			// finished.
			return
		}
		if !parsed.isBuy {
			cache := t.calcAmount1(parsed, oderBooks)
			allCache[parsed.dbpi.PairAddress] = cache
		} else {
			if len(parsed.maker) <= 1 {
				cache := t.calcAmount2(parsed, oderBooks)
				allCache[parsed.dbpi.PairAddress] = cache
			} else {
				var g = make(map[string]*group)
				gr := &group{
					user:   make(map[string]bool),
					piinfo: parsed.dbpi,
				}
				gr.user[parsed.taker.Hex()] = true
				for maker := range parsed.maker {
					gr.user[maker] = true
				}
				g[parsed.dbpi.PairAddress] = gr
				allCache = t.calcAmount3(g, block)
			}
		}
	} else if len(txs) > 1 {
		// 块内有多笔订单簿交易
		var allGroup = make(map[string]*group)
		for _, tx := range txs {
			oderBooks := t.txBooks[tx.Hash().Hex()]
			// parse all taker and maker.
			parsed := t.parseOrdersBook(oderBooks)
			if !parsed.inStatics {
				// finished.
				continue
			}
			if gr, exist := allGroup[parsed.dbpi.PairAddress]; !exist {
				gr = &group{
					user:   make(map[string]bool),
					piinfo: parsed.dbpi,
				}
				gr.user[parsed.taker.Hex()] = true
				for maker := range parsed.maker {
					gr.user[maker] = true
				}
				allGroup[parsed.dbpi.PairAddress] = gr
			} else {
				gr.user[parsed.taker.Hex()] = true
				for maker := range parsed.maker {
					gr.user[maker] = true
				}
			}
		}
		allCache = t.calcAmount3(allGroup, block)
	}

	{
		// 转换结果并送去入库
		for _, cache := range allCache {
			for _, info := range cache {
				record := &model.CompetitionRecord{
					BlockTime: block.Time(),
					TokenPair: info.tokenPair,
					Token:     info.token,
					User:      info.userAddr,
					Celled:    info.cell,
					Buyed:     info.buy,
				}
				records = append(records, record)
			}
		}
		if len(records) > 0 {
			t.saveCh <- records
		}
	}

	for _, tx := range txs {
		// 删除缓存的 orderBooks 信息
		delete(t.txBooks, tx.Hash().Hex())
	}
}

func (t *TradingCampService) task() {
	defer t.wg.Done()

	for !t.dbLoadFinish {
		time.Sleep(time.Millisecond * 50) // wait db load finish.
	}

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

func (t *TradingCampService) monitorNewBlock() {
	defer t.wg.Done()
	defer log.Debug("tradingCampService monitorNewBlock task exit")

	log.Debug("tradingCampService monitorNewBlock task started")

	e := t.tool
	headch := make(chan *types.Header, 10)

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

	sub, err := e.client.SubscribeNewHead(e.ctx, headch)
	if err != nil {
		log.Error("subscribe newHead err ", err)
		return
	}

	orderBookAddress := common.HexToAddress(e.orderBookAddr)

	for {
		select {
		case <-t.closed:
			return
		case nerr := <-sub.Err():
			log.Error("monitorNewBlock got sub.Err, err ", nerr)
			return

		case head := <-headch:
			log.Debug("got new header number ", head.Number)
			{
				block, err := e.client.BlockByNumber(e.ctx, head.Number)
				if err != nil {
					log.Error("get block by number err ", err)
					continue
				}
				fmt.Println("get block by number ", head.Number, "has tx ", len(block.Transactions()))
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
					t.orderCh <- block
				}
			}
		}
	}
}

func (t *TradingCampService) loopNewBlock() {
	defer t.wg.Done()
	defer log.Debug("tradingCampService monitorNewBlock task exit")

	log.Debug("tradingCampService monitorNewBlock task started")

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

type EthTool struct {
	ctx               context.Context
	client            *ethclient.Client
	orderBookAddr     string
	orderBookContract *hpbswap.Contract
}

func NewEthTool(url string, orderBookAddr string) (*EthTool, error) {
	client, err := ethclient.Dial(url)
	if err != nil {
		return nil, err
	}
	c, err := hpbswap.NewContract(common.HexToAddress(orderBookAddr), client)
	if err != nil {
		return nil, err
	}
	return &EthTool{
		ctx:               context.Background(),
		client:            client,
		orderBookContract: c,
		orderBookAddr:     orderBookAddr,
	}, nil
}

func (e *EthTool) GetBlockByTimeStamp(stamp int64) (*big.Int, error) {
	now := time.Now()
	current, err := e.client.BlockNumber(e.ctx)
	if err != nil {
		return big.NewInt(0), err
	}
	if stamp >= now.Unix() {
		return big.NewInt(int64(current)), nil
	}
	maybe := big.NewInt(int64(current) - (now.Unix()-stamp)/3)
	//	fmt.Println("current is ", current, "maybe is ", maybe)
	midBlock, err := e.client.BlockByNumber(e.ctx, maybe)
	if err != nil {
		fmt.Println("get block by may ", maybe, "err = ", err)
		return maybe, err
	}
	if int64(midBlock.Time()) >= stamp {
		maxblock := midBlock
		for int64(maxblock.Time()) > stamp {
			next := maxblock.NumberU64() - 1
			block, err := e.client.BlockByNumber(e.ctx, big.NewInt(int64(next)))
			if err != nil {
				return maxblock.Number(), err
			}
			maxblock = block
		}
		return maxblock.Number(), err
	} else {
		minblock := midBlock
		for int64(minblock.Time()) < stamp {
			next := minblock.NumberU64() + 1
			block, err := e.client.BlockByNumber(e.ctx, big.NewInt(int64(next)))
			if err != nil {
				return minblock.Number(), err
			}
			if int64(block.Time()) > stamp {
				break
			}
			minblock = block
		}
		return minblock.Number(), err
	}
}

type DetailOrderBook struct {
	Maker          common.Address
	FromTokenAddr  common.Address
	OriginalNumber *big.Int
	Timestamp      *big.Int
	CurrentNumber  *big.Int
	ToTokenNumber  *big.Int
	ToTokenSum     *big.Int
}

// 与 o 相比，搜索
func getDetailsOrderBooks(contract *hpbswap.Contract, opts *bind.CallOpts,
	from, to common.Address, index, count *big.Int, user common.Address) ([]DetailOrderBook, error) {
	details, err := contract.GetPageOrderDetailsForMaker(opts, from, to, user, big.NewInt(1), index, count)
	if err != nil {
		return nil, err
	}
	//计算量，价格
	//dbPI := &db.PairInfo{}
	//if err := dbPI.GetByToken(from.Hex(), to.Hex()); err != nil {
	//	return nil, err
	//}
	//log.Info("token0 ", dbPI.Token0, "token1", dbPI.Token1,"dec0", dbPI.Decimals0,"dec1",dbPI.Decimals1)
	orderDetail := make([]DetailOrderBook, len(details.FromTokenAddrs))
	for i := 0; i < len(orderDetail); i++ {
		//orderDetail[i].Maker = details.Makers[i]
		orderDetail[i].FromTokenAddr = details.FromTokenAddrs[i]
		orderDetail[i].OriginalNumber = details.OriginalNumbers[i]
		orderDetail[i].Timestamp = details.Timestamps[i]
		orderDetail[i].CurrentNumber = details.CurrentNumbers[i]
		orderDetail[i].ToTokenNumber = details.ToTokenNumbers[i]
		orderDetail[i].ToTokenSum = details.ToTokenSums[i]
	}
	return orderDetail, nil
}

func (e *EthTool) GetTotalOrderByUser(block *big.Int, user common.Address, fromToken, toToken common.Address) (*big.Int, error) {
	opt := &bind.CallOpts{}
	opt.Context = e.ctx
	opt.BlockNumber = block

	total, err := e.orderBookContract.GetOrderSumsForMaker(opt, fromToken, toToken, user)
	if err != nil {
		return big.NewInt(0), err
	}
	return total, nil
}

func (e *EthTool) GetHistoryByUser(block *big.Int, user common.Address, fromToken, toToken common.Address, start *big.Int, count *big.Int) ([]DetailOrderBook, error) {
	opt := &bind.CallOpts{}
	opt.Context = e.ctx
	opt.BlockNumber = block

	history, err := getDetailsOrderBooks(e.orderBookContract, opt, fromToken, toToken, start, count, user)
	if err != nil {
		return nil, err
	}
	return history, nil
}

func (e *EthTool) GetVolumeByUserWithBlock(block *big.Int, user, fromToken, toToken common.Address) (*big.Int, *big.Int, error) {
	{
		var cell, buy = big.NewInt(0), big.NewInt(0)
		total, err := e.GetTotalOrderByUser(block, user, fromToken, toToken)
		if err != nil {
			fmt.Println("GetTotalOrderByUser failed")
			return nil, nil, err
		}
		// todo: 优化为每次最多取20-50笔记录,循环读取。
		totalDetail, err := e.GetHistoryByUser(block, user, fromToken, toToken, big.NewInt(0), total)
		if err != nil {
			fmt.Println("GetHistoryByUser failed")
			return nil, nil, err
		}
		for _, d := range totalDetail {
			if d.FromTokenAddr == fromToken {
				// 卖出
				celled := new(big.Int).Sub(d.OriginalNumber, d.CurrentNumber)
				cell = new(big.Int).Add(cell, celled)
			} else if d.FromTokenAddr == toToken {
				// 买入
				buy = new(big.Int).Add(buy, d.ToTokenSum)
			}
		}
		return cell, buy, nil
	}
}

func (e *EthTool) GetVolumeByUserBetweenBlock(begin *big.Int, end *big.Int, user, fromToken, toToken common.Address) (*big.Int, *big.Int, error) {
	beginCell, beginBuy, err := e.GetVolumeByUserWithBlock(begin, user, fromToken, toToken)
	if err != nil {
		return nil, nil, err
	}
	endCell, endBuy, err := e.GetVolumeByUserWithBlock(end, user, fromToken, toToken)
	if err != nil {
		return nil, nil, err
	}
	changedCell := new(big.Int).Sub(endCell, beginCell)
	changedBuy := new(big.Int).Sub(endBuy, beginBuy)
	return changedCell, changedBuy, nil
}

func (e *EthTool) MonitorOrderBook() {
	query := ethereum.FilterQuery{
		Addresses: []common.Address{
			common.HexToAddress(e.orderBookAddr),
		},
	}
	logs := make(chan types.Log)
	sub, err := e.client.SubscribeFilterLogs(e.ctx, query, logs)
	if err != nil {
		log.Error("SubscribeFilterLogs failed")
		return
	}
	fmt.Println("start to watch logs")
	for true {
		select {
		case subErr := <-sub.Err():
			log.Error("monitorOrderBook subscription err", subErr)
			continue
		case vlog := <-logs:
			fmt.Println("monitorOrderBook get new log ==============")
			logdata := vlog.Data
			fmt.Println("txhash = ", vlog.TxHash.Hex())
			fmt.Println("log data = ", hex.EncodeToString(logdata))
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

