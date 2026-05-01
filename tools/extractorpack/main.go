package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"goaria-v3/internal/extractor/packbuilder"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: extractorpack hostcall-fixture --out-dir DIR --lock-out PATH")
	}

	switch args[0] {
	case "hostcall-fixture":
		return runHostcallFixture(args[1:], stdout)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runHostcallFixture(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("hostcall-fixture", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	outDir := flags.String("out-dir", filepath.Join("build", "extractor", "cache", "pack_sdk"), "output directory for fixture assets")
	lockOut := flags.String("lock-out", filepath.Join("build", "extractor", "cache", "pack_sdk", "hostcall_fixture.lock.json"), "fixture lock output path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}

	result, err := packbuilder.WriteHostCallFixture(*outDir, *lockOut)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "hostcall fixture pack: %s\n", result.PackZipPath)
	fmt.Fprintf(stdout, "hostcall fixture lock: %s\n", result.LockPath)
	fmt.Fprintf(stdout, "asset_sha256: %s\n", result.Assets.AssetSHA256)
	fmt.Fprintf(stdout, "public_key: %x\n", result.Assets.PublicKey)

	return nil
}
