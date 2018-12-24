// This program generates decorators that wrap interfaces. Useful for all manner of things.

package decgen

import (
	"fmt"
	"go/ast"
	"go/types"
	"log"
	"path/filepath"
	"strings"

	"github.com/gojuno/generator"
	"github.com/pkg/errors"
	"golang.org/x/tools/go/loader"
)

type validatorFunc func(s *types.Signature) error

// Generator is capable of generating a decorator using its template.
type Generator struct {
	template   string
	validators []validatorFunc
}

// NewGenerator returns a new generator.
func NewGenerator(template string, validators ...validatorFunc) *Generator {
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
			return "", fmt.Errorf("the specified interface wasn't a client")
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

			switch asserted := result.OriginalType.(type) {
			case *types.Named:
				switch asserted.Underlying().String() {
				case "int64":
					emptyValue = "0"
				case "string":
					emptyValue = `""`
				case "bool":
					emptyValue = "false"
				default:
					panic(fmt.Sprintf("no nil value for named type known: %s, %s", asserted.String(), asserted.Underlying().String()))
				}
			case *types.Basic:
				switch asserted.Name() {
				case "int64":
					emptyValue = "0"
				case "string":
					emptyValue = `""`
				case "bool":
					emptyValue = "false"
				default:
					panic(fmt.Sprintf("no nil value for basic type known: %s", asserted.Name()))
				}

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
