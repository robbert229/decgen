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

Downloading `decgen` is done using go get.

```bash
go get github.com/robbert229/decgen
```

### trace

To use `decgen` to generate a decorator first you need to create your interface. 

```go
type Service interface {
    Do(ctx, *Request) (*Response, error)
}
```

```bash

decgen -i Service -s TracingService -o ./tracer.go -t trace .

```
