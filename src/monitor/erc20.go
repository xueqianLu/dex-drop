package monitor

import (
	"context"
	"fmt"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/hpdex-project/dex-drop/src/config"
	"github.com/hpdex-project/dex-drop/src/contracts/factory"
	log "github.com/sirupsen/logrus"
	"math/big"
	"time"
)

var (
	zeroBig           = big.NewInt(0)
	UserAmounts       = make(map[common.Address]*big.Int)
	OverThresholdUser = make(map[common.Address]uint64)
	decmical          = big.NewInt(0).Exp(big.NewInt(10), big.NewInt(18), nil)
	threshold         = big.NewInt(0).Mul(big.NewInt(100), decmical)
)

func userAdd(addr common.Address, amount *big.Int) *big.Int {
	var uamount *big.Int
	var exist bool
	if uamount, exist = UserAmounts[addr]; !exist {
		uamount = big.NewInt(0).Add(big.NewInt(0), amount)
	} else {
		uamount = big.NewInt(0).Add(uamount, amount)
	}
	UserAmounts[addr] = uamount
	return uamount
}

func SwapParse(url string, begin, end int64) {
	client, err := ethclient.Dial(url)
	if err != nil {
		log.Fatal(err)
	}

	pairAddr := config.WHPB_USDT_PAIR
	//usdtAddr := config.USDT_Contract
	//hpbAddr  := config.WHPB_Contract

	page := int64(5000)
	swapFilter, err := factory.NewIUniswapV2PairFilterer(pairAddr, client)
	if err != nil {
		log.Fatal("NewIUniswapV2PairFilterer failed, err:", err)
	}
	for begin < end {
		var fromBlock = uint64(begin)
		var endBlock = uint64(end)
		if (end - begin) > page {
			endBlock = uint64(begin + page)
		}
		fmt.Printf("goto filter logs from(%d) to(%d)\n", fromBlock, endBlock)
		p := &bind.FilterOpts{
			Start:   uint64(begin),
			End:     &endBlock,
			Context: context.Background(),
		}

		swapiter, err := swapFilter.FilterSwap(p, []common.Address{}, []common.Address{})
		if err != nil {
			fmt.Printf("filter swap failed, err :", err)
			return
		}
		for swapiter.Next() {
			event := swapiter.Event
			//fmt.Printf("evnet :%v\n", event)
			//{
			//	fmt.Printf("sender: %s\n", event.Sender.Hex())
			//	fmt.Printf("amount0In: %s\n", event.Amount0In.Text(10))
			//	fmt.Printf("amount1In: %s\n", event.Amount1In.Text(10))
			//	fmt.Printf("amount0Out: %s\n", event.Amount0Out.Text(10))
			//	fmt.Printf("amount1Out: %s\n", event.Amount1Out.Text(10))
			//	fmt.Printf("to: %s\n", event.To.Hex())
			//}
			var nowAmount *big.Int
			var user common.Address
			var amount *big.Int
			if event.Amount1In.Cmp(zeroBig) > 0 {
				user = event.Sender
				amount = event.Amount1In
			} else if event.Amount1Out.Cmp(zeroBig) > 0 {
				user = event.To
				amount = event.Amount1Out
			}
			nowAmount = userAdd(user, amount)
			if nowAmount.Cmp(threshold) >= 0 {
				if _, exist := OverThresholdUser[user]; !exist {
					OverThresholdUser[user] = event.Raw.BlockNumber
					fmt.Printf("user %s transfer amount over %s at block %d\n", user.String(), threshold, event.Raw.BlockNumber)
				}
			} else {
				fmt.Printf("user %s transfer amount %s total at block %d\n", user.String(), nowAmount.Text(10), event.Raw.BlockNumber)
			}
		}
		begin = int64(endBlock + 1)
		time.Sleep(time.Second)
	}
}
