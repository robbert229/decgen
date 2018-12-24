# decgen

## Introduction

`decgen` can generate boilerplate code to handle

* tracing requests using opentrace,
* manage database/sql transactions,
* create adapters to use generated grpc servers as clients,
* protect the methods on a struct with a mutex,

`decgen` primarily works by generating implementations of interfaces that
utilize other implementations of those interfaces.

## usage

Add the following file to your project under `scripts/decgen.go`

```go
// +build ignore

package main

import "github.com/robbert229/decgen/cmd"

func main() {
	cmd.Execute()
}
```

`decgen` then needs to be installed with either `go get`, or `dep`. 

Add the following file to your project under `./service.go`

```go
package pkg

type Service interface {
    Do(ctx, *Request) (*Response, error)
}
```

Then run `decgen` by calling `go run ./scripts/decgen.go -i Service -s TracingService -o ./tracer.go -t trace .`
This will run `decgen` generating a tracing decorator for the given `Service` interface. 
