package main

import (
	"github.com/alecthomas/kong"
)

// CLI is the top-level command structure for clibench.
type CLI struct {
	Bench     BenchCmd     `cmd:"" help:"Run transport benchmarks."`
	Server    ServerCmd    `cmd:"" help:"Start standalone multi-protocol server."`
	Smoketest SmoketestCmd `cmd:"" help:"Quick integration smoke test."`
}

func main() {
	var cli CLI
	ctx := kong.Parse(&cli,
		kong.Name("clibench"),
		kong.Description("SSH vs HTTPS vs HTTP/3 CLI transport benchmark."),
		kong.UsageOnError(),
	)
	ctx.FatalIfErrorf(ctx.Run())
}
