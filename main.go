package main

import (
	"flag"
	"github.com/hpdex-project/dex-drop/src/monitor"
)

func main() {
	u := flag.String("url", "http://127.0.0.1:8545", "rpc url")
	begin := flag.Int64("s", 0, "start block to filter")
	end := flag.Int64("e", 0, "end block to filter")
	flag.Parse()

	monitor.SwapParse(*u, *begin, *end)
}
