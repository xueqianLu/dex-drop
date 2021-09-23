package monitor

import (
	"context"
	"fmt"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/hpdex-project/dex-drop/src/config"
	"github.com/hpdex-project/dex-drop/src/contracts/factory"
	log "github.com/sirupsen/logrus"
	"math/big"
	"strings"
)

// LogTransfer ..
type LogTransfer struct {
	From   common.Address
	To     common.Address
	Tokens *big.Int
}

// LogApproval ..
type LogApproval struct {
	TokenOwner common.Address
	Spender    common.Address
	Tokens     *big.Int
}

type LogSwap struct {
	Sender     common.Address
	Amount0In  *big.Int
	Amount1In  *big.Int
	Amount0Out *big.Int
	Amount1Out *big.Int
	To         common.Address
}

func SwapParse(url string, begin, end int64) {
	client, err := ethclient.Dial(url)
	if err != nil {
		log.Fatal(err)
	}

	// 0x Protocol (ZRX) token address
	pairAddr := config.WHPB_USDT_PAIR
	page := int64(5000)
	for begin < end {
		var fromBlock = big.NewInt(begin)
		var toBlock = big.NewInt(end)
		if (end - begin) > page {
			toBlock = big.NewInt(begin + page)
		}
		query := ethereum.FilterQuery{
			FromBlock: fromBlock,
			ToBlock:   toBlock,
			Addresses: []common.Address{
				pairAddr,
			},
		}

		logs, err := client.FilterLogs(context.Background(), query)
		if err != nil {
			log.Fatal(err)
		}

		contractAbi, err := abi.JSON(strings.NewReader(string(factory.IUniswapV2PairABI)))
		if err != nil {
			log.Fatal(err)
		}

		logSwapSig := []byte("Swap(address,uint256,uint256,uint256,uint256,address)")
		logSwapSigHash := crypto.Keccak256Hash(logSwapSig)

		for _, vLog := range logs {
			fmt.Printf("Log Block Number: %d\n", vLog.BlockNumber)
			fmt.Printf("Log Index: %d\n", vLog.Index)

			switch vLog.Topics[0].Hex() {
			case logSwapSigHash.Hex():
				fmt.Printf("Log Name: Transfer\n")

				events, err := contractAbi.Unpack("Swap", vLog.Data)
				if err != nil {
					log.Fatal(err)
				}
				for _, event := range events {
					if swapEvent, ok := event.(LogSwap); ok {
						fmt.Printf("sender: %s\n", swapEvent.Sender.Hex())
						fmt.Printf("amount0In: %s\n", swapEvent.Amount0In.Text(10))
						fmt.Printf("amount1In: %s\n", swapEvent.Amount1In.Text(10))
						fmt.Printf("amount0Out: %s\n", swapEvent.Amount0Out.Text(10))
						fmt.Printf("amount1Out: %s\n", swapEvent.Amount1Out.Text(10))
						fmt.Printf("to: %s\n", swapEvent.To.Hex())
					}
				}
			}

			fmt.Printf("\n\n")
		}
	}
}
