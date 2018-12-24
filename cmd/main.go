package cmd

import (
	"flag"
	"fmt"
	"go/types"
	"log"
	"os"

	"github.com/pkg/errors"
	"github.com/robbert229/decgen"
)

var (
	interfaceName = flag.String("i", "", "interface name")
	structName    = flag.String("s", "", "target struct name, default: <interface name>Tracer")
	outputFile    = flag.String("o", "", "output filename")
	decType       = flag.String("t", "", "decorator type [trace,sqltx,grpcadapter,mutex]")
)

func Execute() {
	flag.Parse()

	if *interfaceName == "" || *outputFile == "" || flag.NArg() != 1 || *decType == "" {
		flag.Usage()
		os.Exit(1)
	}

	template, ok := decgen.Templates[*decType]
	if !ok {
		flag.Usage()
		os.Exit(1)
	}

	if *structName == "" {
		*structName = fmt.Sprintf("%vTracer", *interfaceName)
	}

	fn := func(s *types.Signature) error {
		if s.Params().Len() == 0 || s.Params().At(0).Type().String() != "context.Context" {
			return errors.Errorf("first param must be context.Context")
		}

		return nil
	}

	gen := decgen.NewGenerator(template, fn)
	err := gen.Generate(template, flag.Arg(0), *interfaceName, *outputFile, *structName)
	if err != nil {
		log.Fatal(err)
	}
}
