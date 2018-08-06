// This program generates decorators that wrap interfaces. Useful for all manner of things.

package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/types"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/gojuno/generator"
	"github.com/pkg/errors"
	"golang.org/x/tools/go/loader"
	)

var (
	interfaceName = flag.String("i", "", "interface name")
	structName    = flag.String("s", "", "target struct name, default: <interface name>Tracer")
	outputFile    = flag.String("o", "", "output filename")
	decType       = flag.String("t", "", "decorator type [trace,sqltx,grpcadapter,mutex]")
)

const mutexTemplate = `
        // {{$structName}} ensures that only one call can be active at a time using a mutext.
        type {{$structName}} struct {
                next {{$interfaceName}}
                mutex *sync.Mutex
        }

        // New{{$structName}} returns a new {{$structName}}
        func New{{$structName}}(next {{$interfaceName}}) *{{$structName}} {
                return &{{$structName}} {
                        next: next,
                        mutex: new(sync.Mutex),
                }
        }

        {{ range $methodName, $method := .}}
                // {{$methodName}} wraps the underlying {{$interfaceName}} implementation with a mutex.
                func (t *{{$structName}}) {{$methodName}}{{signature $method}} {
                        t.mutex.Lock()
                        defer t.mutex.Unlock()

                        var err error
                        {{results $method}} t.next.{{$methodName}}({{call $method}})
                        if err != nil {
                                return {{returnerr $method}} errors.WithStack(err)
                        }

                        return {{returnok $method}}
                }
        {{ end }}
`

const traceTemplate = `
        // {{$structName}} traces any calls to the next service using opentracing.
        type {{$structName}} struct {
                next {{$interfaceName}}
                prefix string
        }
        // New{{$structName}} returns a new {{$structName}}
        func New{{$structName}}(next {{$interfaceName}}, prefix string) *{{$structName}} {
                return &{{$structName}} {
                        next: next,
                        prefix: prefix,
                }
        }

        {{ range $methodName, $method := . }}
                // {{$methodName}} wraps the underlying {{$interfaceName}} implementation with transactions.
                func (t *{{$structName}}) {{$methodName}}{{signature $method}} {
                        span, ctx := opentracing.StartSpanFromContext(ctx, t.prefix + ".{{$interfaceName}}.{{$methodName}}")
                        defer span.Finish()

                        var err error
                        {{results $method}} t.next.{{$methodName}}({{call $method}})
                        if err != nil {
                                return {{returnerr $method}} errors.WithStack(err)
                        }

                        return {{returnok $method}}
                }
        {{ end }}
`

const grpcadapterTemplate = `
        // {{$structName}} is an adaptor that allows a generated grpc server to be utilized like a client.
        type {{$structName}} struct {
                next {{grpcadapterclient $interfaceName}}
        }

        // New{{$structName}} returns a new {{$structName}}
        func New{{$structName}}(next {{grpcadapterclient $interfaceName}}) *{{$structName}} {
                return &{{$structName}} {
                        next: next,
                }
        }

        {{ range $methodName, $method := . }}
                // {{$methodName}} wraps the underlying {{$interfaceName}} and removing the grpc call options.
                func (t *{{$structName}}) {{$methodName}}{{signature $method}} {
                        var err error
                        {{results $method}} t.next.{{$methodName}}({{callgrpcadapter $method}})
                        if err != nil {
                                return {{returnerr $method}} errors.WithStack(err)
                        }

                        return {{returnok $method}}
                }
        {{ end }}

`

const sqltxTemplate = `
        // {{$structName}} manages transactions around repository pattern structs.
        type {{$structName}} struct {
                db *sql.DB
                constructor func(tx sql.Tx) {{$interfaceName}}
        }
        // New{{$structName}} returns a new {{$structName}}
        func New{{$structName}}(db *sql.DB, constructor func(db boil.Executor) {{$interfaceName}}) *{{$structName}} {
                return &{{$structName}} {
                        db: db,
                        constructor: constructor,
                }
        }

        {{ range $methodName, $method := . }}
                // {{$methodName}} wraps the underlying {{$interfaceName}} implementation with transactions.
                func (t *{{$structName}}) {{$methodName}}{{signature $method}} {
                        {{if mutates $methodName}}
                                tx, err := t.db.Begin()
                                if err != nil {
                                        return {{returnerr $method}} errors.New("unable to start transaction")
                                }

                                repository := t.constructor(tx)
                                {{results $method}} repository.{{$methodName}}({{call $method}})
                                if err != nil {
                                        txErr := tx.Rollback()
                                        if txErr != nil {
                                                // handle rollback failed
                                                return {{returnerr $method}} errors.Wrapf(err, "failed to rollback transaction for error (%v)", txErr)
                                        }
                                        return {{returnerr $method}} errors.WithStack(err)
                                }

                                err = tx.Commit()
                                if err != nil {
                                        // handle failed commit
                                        return {{returnerr $method}} errors.WithStack(err)
                                }

                                return {{returnok $method}}
                        {{ else }}
                                repository := t.constructor(t.db)

                                {{results $method}} repository.{{$methodName}}({{call $method}})
                                if err != nil {
                                        return {{returnerr $method}} errors.WithStack(err)
                                }

                                return {{returnok $method}}
                        {{ end }}
                }
        {{ end }}
        `

var (
	templates = map[string]string{
		"sqltx":       sqltxTemplate,
		"trace":       traceTemplate,
		"grpcadapter": grpcadapterTemplate,
		"mutex":       mutexTemplate,
	}
)

func main() {
	flag.Parse()

	if *interfaceName == "" || *outputFile == "" || flag.NArg() != 1 || *decType == "" {
		flag.Usage()
		os.Exit(1)
	}

	template, ok := templates[*decType]
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

	gen := NewGenerator(template, []validatorFunc{fn})
	err := gen.Generate(template, flag.Arg(0), *interfaceName, *outputFile, *structName)
	if err != nil {
		log.Fatal(err)
	}
}

type validatorFunc func(s *types.Signature) error

// Generator is capable of generating a decorator using its template.
type Generator struct {
	template   string
	validators []validatorFunc
}

// NewGenerator returns a new generator.
func NewGenerator(template string, validators []validatorFunc) *Generator {
	return &Generator{
		template:   template,
		validators: validators,
	}
}

// Generate generates an implementation of an interface that decorates another implementation of the interface using transactions.
func (g *Generator) Generate(template, sourcePackage, interfaceName, outputFile, outputStruct string) error {
	sourcePath, err := generator.PackageOf(sourcePackage)
	if err != nil {
		return errors.Wrap(err, "failed to get sourcePath")
	}

	destPath, err := generator.PackageOf(filepath.Dir(outputFile))
	if err != nil {
		return errors.Wrap(err, "failed to get destPath")
	}

	program, err := g.createProgram(sourcePath, destPath)
	if err != nil {
		return errors.Wrap(err, "failed to create program")
	}

	gen := generator.New(program)

	_, sourcePackageName := gen.PackagePathAndName(sourcePath)
	_, destPackageName := gen.PackagePathAndName(destPath)

	gen.SetPackageName(destPackageName)
	gen.SetVar("structName", outputStruct)
	gen.AddTemplateFunc("call", FuncCall(gen))
	gen.AddTemplateFunc("signature", FuncSignature(gen))
	gen.AddTemplateFunc("returnerr", FuncReturnErr(gen))
	gen.AddTemplateFunc("results", FuncResults(gen))
	gen.AddTemplateFunc("returnok", FuncReturnOK(gen))
	gen.AddTemplateFunc("callgrpcadapter", FuncCallGRPCAdapter(gen))
	gen.AddTemplateFunc("mutates", FuncMutates(gen))
	gen.AddTemplateFunc("grpcadapterclient", FuncGRPCAdapterServer(gen))

	gen.ImportWithAlias(destPath, "")

	if sourcePath != destPath {
		gen.SetVar("interfaceName", fmt.Sprintf("%v.%v", sourcePackageName, interfaceName))
	} else {
		gen.SetVar("interfaceName", interfaceName)
	}

	v := &visitor{
		gen:        gen,
		methods:    make(map[string]*types.Signature),
		sname:      interfaceName,
		validators: g.validators,
	}
	for _, file := range program.Package(sourcePath).Files {
		ast.Walk(v, file)
	}

	if v.err != nil {
		return errors.Wrap(v.err, "failed to parse interface")
	}

	if err := gen.ProcessTemplate("interface", template, v.methods); err != nil {
		return errors.Wrap(err, "failed to process template")
	}

	if err := gen.WriteToFilename(outputFile); err != nil {
		return errors.Wrap(err, "failed to write file")
	}

	return nil
}


// createProgram creates the program.
func (g *Generator) createProgram(sourcePath, destPath string) (*loader.Program, error) {
	config := loader.Config{}

	config.Import(sourcePath)
	config.Import(destPath)
	config.Import("github.com/volatiletech/sqlboiler/boil")

	return config.Load()
}

// visitor collects all methods of specified interface
type visitor struct {
	gen        *generator.Generator
	methods    map[string]*types.Signature
	sname      string
	validators []validatorFunc
	err        error
}

// Visit is implementation of ast.Visitor interface
func (v *visitor) Visit(node ast.Node) (w ast.Visitor) {
	if ts, ok := node.(*ast.TypeSpec); ok {
		exprType, err := v.gen.ExpressionType(ts.Type)
		if err != nil {
			log.Fatal(err)
		}

		switch t := exprType.(type) {
		case *types.Interface:
			if ts.Name.Name != v.sname {
				return v
			}

			if v.err == nil {
				v.err = v.processInterface(t)
			}
		}
	}

	return v
}

// processInterface processes the interface.
func (v *visitor) processInterface(t *types.Interface) error {
	for i := 0; i < t.NumMethods(); i++ {
		name := t.Method(i).Name()
		signature := t.Method(i).Type().(*types.Signature)
		for _, validator := range v.validators {
			err := validator(signature)
			if err != nil {
				return errors.Wrapf(err, "failed to validate method '%v'", name)
			}
		}
		v.methods[name] = signature
	}

	return nil
}

// paramName returns the name of the parameter for a given its index, and the list of parameters.
func paramName(index int, paramSet generator.ParamSet) string {
	param := paramSet[index]

	// if the first item is a context.Context then it should be named ctx.
	if param.Type == "context.Context" && index == 0 {
		return "ctx"
	}

	// if the second item is the last item then it should be named req.
	if len(paramSet) == 2 && index == 1 {
		return "req"
	}

	return fmt.Sprintf("param%d", index)
}

// FuncSignature builds the interface of the method.
func FuncSignature(g *generator.Generator) interface{} {
	return func(f interface{}) (string, error) {
		params, err := g.FuncParams(f)
		if err != nil {
			return "", fmt.Errorf("failed to get %+v func params: %v", f, err)
		}

		names := []string{}
		for i, param := range params {
			paramName := paramName(i, params)

			paramType := param.Type
			if strings.HasSuffix(param.Pass(), "...") {
				paramType = "..." + strings.TrimPrefix(paramType, "[]")
			}

			names = append(names, fmt.Sprintf("%s %s", paramName, paramType))
		}

		results, err := g.FuncResults(f)
		if err != nil {
			return "", fmt.Errorf("failed to get %+v func results: %v", f, err)
		}

		returns := []string{}
		for _, result := range results {
			returns = append(returns, result.Type)
		}

		return "(" + strings.Join(names, ", ") + ") (" + strings.Join(returns, ", ") + ")", nil
	}
}

//FuncCall returns a signature of the function represented by f
//f can be one of: ast.Expr, ast.SelectorExpr, types.Type, types.Signature
func FuncCall(g *generator.Generator) interface{} {
	return func(f interface{}) (string, error) {
		params, err := g.FuncParams(f)
		if err != nil {
			return "", fmt.Errorf("failed to get %+v func params: %v", f, err)
		}

		names := []string{}
		for i := range params {
			names = append(names, paramName(i, params))
		}

		return strings.Join(names, ", "), nil
	}
}

// FuncGRPCAdapterServer returns the name of the grpc server given the client name.
func FuncGRPCAdapterServer(g *generator.Generator) interface{} {
	return func(server interface{}) (string, error) {
		s, ok := server.(string)
		if !ok {
			return "", fmt.Errorf("didn't recieve a string: %+v", server)
		}

		if !strings.HasSuffix(s, "Client") {
			return "", fmt.Errorf("didn't recieve a client")
		}

		s = strings.TrimSuffix(s, "Client")
		s += "Server"

		return s, nil
	}
}

// FuncCallGRPCAdapter returns the parameters used to adapt a grpc client call to a grpc server.
func FuncCallGRPCAdapter(g *generator.Generator) interface{} {
	return func(f interface{}) (string, error) {
		params, err := g.FuncParams(f)
		if err != nil {
			return "", fmt.Errorf("failed to get %+v func params: %v", f, err)
		}

		names := []string{}
		for i := range params {
			if i == len(params)-1 {
				break
			}
			names = append(names, paramName(i, params))
		}

		return strings.Join(names, ", "), nil
	}
}

// FuncReturnErr creates a list of results using their nil values excluding
// the last value if it is an error. This allows users to return a wrapped
// error, or what ever they so desire.
func FuncReturnErr(g *generator.Generator) interface{} {
	return func(f interface{}) (string, error) {
		results, err := g.FuncResults(f)
		if err != nil {
			return "", fmt.Errorf("failed to get %+v func params: %v", f, err)
		}

		emptyValues := []string{}
		for i, result := range results {

			// if the return type is error and its the last return then end.
			if result.Type == "error" && i == len(results)-1 {
				break
			}

			emptyValue := "nil"

			switch result.Type {

			}

			emptyValues = append(emptyValues, emptyValue)
		}

		if len(emptyValues) == 0 {
			return "", nil
		}

		return strings.Join(emptyValues, ", ") + ",", nil
	}
}

// FuncResults returns the slice of results being returned from a function as
// well as the assignment operator used.
func FuncResults(g *generator.Generator) interface{} {
	return func(f interface{}) (string, error) {
		results, err := g.FuncResults(f)
		if err != nil {
			return "", fmt.Errorf("failed to get %+v func params: %v", f, err)
		}

		resultArguments := []string{}
		for i, result := range results {
			if result.Type == "error" && i == len(results)-1 {
				resultArguments = append(resultArguments, "err")
			} else {
				resultArguments = append(resultArguments, fmt.Sprintf("arg%d", i))
			}
		}

		assignmentOperator := ":="
		if len(resultArguments) == 1 && results[0].Type == "error" {
			assignmentOperator = "="
		}

		return fmt.Sprintf("%s %s ", strings.Join(resultArguments, ", "), assignmentOperator), nil
	}
}

// FuncMutates returns a boolean that denotes if a given function mutates things.
func FuncMutates(g *generator.Generator) interface{} {
	return func(f interface{}) (bool, error) {
		// name := f.(string)
		return true, nil
	}
}

// FuncReturnOK returns the slice of results from a function where if the last
// result is an error it is replaced with nil.
func FuncReturnOK(g *generator.Generator) interface{} {
	return func(f interface{}) (string, error) {
		results, err := g.FuncResults(f)
		if err != nil {
			return "", fmt.Errorf("failed to get %+v func params: %v", f, err)
		}

		resultArguments := []string{}
		for i, result := range results {
			if result.Type == "error" && i == len(results)-1 {
				resultArguments = append(resultArguments, "nil")
			} else {
				resultArguments = append(resultArguments, fmt.Sprintf("arg%d", i))
			}
		}

		return strings.Join(resultArguments, ", "), nil
	}
}
