package decgen

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
		constructor func(tx *sql.Tx) {{$interfaceName}}
}
// New{{$structName}} returns a new {{$structName}}
func New{{$structName}}(db *sql.DB, constructor func(tx *sql.Tx) {{$interfaceName}}) *{{$structName}} {
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
	
			defer func(){
				if p := recover(); p != nil {
					tx.Rollback()
					panic(p)
				}
			}()

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
	Templates = map[string]string{
		"sqltx":       sqltxTemplate,
		"trace":       traceTemplate,
		"grpcadapter": grpcadapterTemplate,
		"mutex":       mutexTemplate,
	}
)
