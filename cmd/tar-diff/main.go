package main

import (
	"flag"
	"fmt"
	"github.com/alexlarsson/tar-diff/pkg/common"
	"github.com/alexlarsson/tar-diff/pkg/tar-diff"
	"os"
	"path"
)

var version = flag.Bool("version", false, "Show version")
var compressionLevel = flag.Int("compression-level", 3, "zstd compression level (default 3)")

func main() {

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [OPION] old.tar.gz new.tar.gz result.tardiff\n", path.Base(os.Args[0]))
		fmt.Fprintf(flag.CommandLine.Output(), "Options:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	if *version {
		fmt.Printf("%s %s\n", path.Base(os.Args[0]), common.VERSION)
		return
	}

	if flag.NArg() != 3 {
		flag.Usage()
		os.Exit(1)
	}

	oldFilename := flag.Arg(0)
	newFilename := flag.Arg(1)
	deltaFilename := flag.Arg(2)

	oldFile, err := os.Open(oldFilename)
	if err != nil {
		fmt.Fprintf(flag.CommandLine.Output(), "Unable to open %s: %s\n", oldFilename, err)
		os.Exit(1)
	}
	defer oldFile.Close()

	newFile, err := os.Open(newFilename)
	if err != nil {
		fmt.Fprintf(flag.CommandLine.Output(), "Unable to open %s: %s\n", newFilename, err)
		os.Exit(1)
	}
	defer newFile.Close()

	deltaFile, err := os.Create(deltaFilename)
	if err != nil {
		fmt.Fprintf(flag.CommandLine.Output(), "Unable to create %s: %s\n", deltaFilename, err)
		os.Exit(1)
	}

	options := tar_diff.NewOptions()
	options.SetCompressionLevel(*compressionLevel)

	err = tar_diff.Diff(oldFile, newFile, deltaFile, options)
	if err != nil {
		fmt.Fprintf(flag.CommandLine.Output(), "Error generating delta: %s\n", err)
		os.Exit(1)
	}

	err = deltaFile.Close()
	if err != nil {
		fmt.Fprintf(flag.CommandLine.Output(), "Error generating delta: %s\n", err)
	}

}
