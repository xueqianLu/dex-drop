package monitor

import (
	"context"
	"fmt"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/hpdex-project/dex-drop/src/contracts/router"
	"math/big"
	"time"
)

type EthTool struct {
	ctx            context.Context
	client         *ethclient.Client
	routerAddr     string
	routerContract *router.UniswapV2Router02
}

func NewEthTool(url string, routerAddr string) (*EthTool, error) {
	client, err := ethclient.Dial(url)
	if err != nil {
		return nil, err
	}

	c, err := router.NewUniswapV2Router02(common.HexToAddress(routerAddr), client)
	if err != nil {
		return nil, err
	}
	return &EthTool{
		ctx:            context.Background(),
		client:         client,
		routerContract: c,
		routerAddr:     routerAddr,
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
