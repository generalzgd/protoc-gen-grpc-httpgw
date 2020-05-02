package gengateway

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/golang/glog"
	generator2 "github.com/golang/protobuf/protoc-gen-go/generator"
	"github.com/grpc-ecosystem/grpc-gateway/utilities"
	`github.com/toolkits/slice`

	"github.com/generalzgd/protoc-gen-grpc-httpgw/descriptor"
)

type param struct {
	*descriptor.File
	Imports            []descriptor.GoPackage
	UseRequestContext  bool
	RegisterFuncSuffix string
	AllowPatchFeature  bool
	AdditionImports    []string
}

type binding struct {
	*descriptor.Binding
	Registry          *descriptor.Registry
	AllowPatchFeature bool
}

// GetBodyFieldPath returns the binding body's fieldpath.
func (b binding) GetBodyFieldPath() string {
	if b.Body != nil && len(b.Body.FieldPath) != 0 {
		return b.Body.FieldPath.String()
	}
	return "*"
}

// HasQueryParam determines if the binding needs parameters in query string.
//
// It sometimes returns true even though actually the binding does not need.
// But it is not serious because it just results in a small amount of extra codes generated.
func (b binding) HasQueryParam() bool {
	if b.Body != nil && len(b.Body.FieldPath) == 0 {
		return false
	}
	fields := make(map[string]bool)
	for _, f := range b.Method.RequestType.Fields {
		fields[f.GetName()] = true
	}
	if b.Body != nil {
		delete(fields, b.Body.FieldPath.String())
	}
	for _, p := range b.PathParams {
		delete(fields, p.FieldPath.String())
	}
	return len(fields) > 0
}

func (b binding) QueryParamFilter() queryParamFilter {
	var seqs [][]string
	if b.Body != nil {
		seqs = append(seqs, strings.Split(b.Body.FieldPath.String(), "."))
	}
	for _, p := range b.PathParams {
		seqs = append(seqs, strings.Split(p.FieldPath.String(), "."))
	}
	return queryParamFilter{utilities.NewDoubleArray(seqs)}
}

// HasEnumPathParam returns true if the path parameter slice contains a parameter
// that maps to an enum proto field that is not repeated, if not false is returned.
func (b binding) HasEnumPathParam() bool {
	return b.hasEnumPathParam(false)
}

// HasRepeatedEnumPathParam returns true if the path parameter slice contains a parameter
// that maps to a repeated enum proto field, if not false is returned.
func (b binding) HasRepeatedEnumPathParam() bool {
	return b.hasEnumPathParam(true)
}

// hasEnumPathParam returns true if the path parameter slice contains a parameter
// that maps to a enum proto field and that the enum proto field is or isn't repeated
// based on the provided 'repeated' parameter.
func (b binding) hasEnumPathParam(repeated bool) bool {
	for _, p := range b.PathParams {
		if p.IsEnum() && p.IsRepeated() == repeated {
			return true
		}
	}
	return false
}

// LookupEnum looks up a enum type by path parameter.
func (b binding) LookupEnum(p descriptor.Parameter) *descriptor.Enum {
	e, err := b.Registry.LookupEnum("", p.Target.GetTypeName())
	if err != nil {
		return nil
	}
	return e
}

// FieldMaskField returns the golang-style name of the variable for a FieldMask, if there is exactly one of that type in
// the message. Otherwise, it returns an empty string.
func (b binding) FieldMaskField() string {
	var fieldMaskField *descriptor.Field
	for _, f := range b.Method.RequestType.Fields {
		if f.GetTypeName() == ".google.protobuf.FieldMask" {
			// if there is more than 1 FieldMask for this request, then return none
			if fieldMaskField != nil {
				return ""
			}
			fieldMaskField = f
		}
	}
	if fieldMaskField != nil {
		return generator2.CamelCase(fieldMaskField.GetName())
	}
	return ""
}

// queryParamFilter is a wrapper of utilities.DoubleArray which provides String() to output DoubleArray.Encoding in a stable and predictable format.
type queryParamFilter struct {
	*utilities.DoubleArray
}

func (f queryParamFilter) String() string {
	encodings := make([]string, len(f.Encoding))
	for str, enc := range f.Encoding {
		encodings[enc] = fmt.Sprintf("%q: %d", str, enc)
	}
	e := strings.Join(encodings, ", ")
	return fmt.Sprintf("&utilities.DoubleArray{Encoding: map[string]int{%s}, Base: %#v, Check: %#v}", e, f.Base, f.Check)
}

type trailerParams struct {
	Services           []*descriptor.Service
	UseRequestContext  bool
	RegisterFuncSuffix string
	AssumeColonVerb    bool
}

func applyTemplate(p param, reg *descriptor.Registry) (string, error) {
	w := bytes.NewBuffer(nil)
	var addiImport []string
	for _, svc := range p.Services {
		for _, im := range svc.ParseAdditionalImport() {
			if !slice.ContainsString(addiImport, im) {
				addiImport = append(addiImport, im)
			}
		}
	}
	p.AdditionImports = addiImport
	if err := headerTemplate.Execute(w, p); err != nil {
		return "", err
	}

	var targetServices []*descriptor.Service

	for _, msg := range p.Messages {
		msgName := generator2.CamelCase(*msg.Name)
		msg.Name = &msgName
	}
	for _, svc := range p.Services {
		var methodWithBindingsSeen bool
		svcName := generator2.CamelCase(*svc.Name)
		svc.Name = &svcName



		for _, meth := range svc.Methods {
			glog.V(2).Infof("Processing %s.%s", svc.GetName(), meth.GetName())
			methName := generator2.CamelCase(*meth.Name)
			meth.Name = &methName
			for _, b := range meth.Bindings {
				methodWithBindingsSeen = true
				if err := handlerTemplate.Execute(w, binding{
					Binding:           b,
					Registry:          reg,
					AllowPatchFeature: p.AllowPatchFeature,
				}); err != nil {
					return "", err
				}
			}
		}
		if methodWithBindingsSeen {
			targetServices = append(targetServices, svc)
		}
	}
	if len(targetServices) == 0 {
		return "", errNoTargetService
	}



	assumeColonVerb := true
	if reg != nil {
		assumeColonVerb = !reg.GetAllowColonFinalSegments()
	}
	tp := trailerParams{
		Services:           targetServices,
		UseRequestContext:  p.UseRequestContext,
		RegisterFuncSuffix: p.RegisterFuncSuffix,
		AssumeColonVerb:    assumeColonVerb,
	}
	if err := trailerTemplate.Execute(w, tp); err != nil {
		return "", err
	}
	return w.String(), nil
}

var (
	headerTemplate = template.Must(template.New("header").Parse(`
// Code generated by protoc-gen-grpc-gateway. DO NOT EDIT.
// source: {{.GetName}}

/*
Package {{.GoPkg.Name}} is a reverse proxy.

It translates gRPC into RESTful JSON APIs.
*/
package {{.GoPkg.Name}}
import (
	{{range $i := .Imports}}{{if $i.Standard}}{{$i | printf "%s\n"}}{{end}}{{end}}

	{{range $i := .Imports}}{{if not $i.Standard}}{{$i | printf "%s\n"}}{{end}}{{end}}

	{{range $i := .AdditionImports}}
		"{{$i}}"{{end}}
)

var _ codes.Code
var _ io.Reader
var _ status.Status
var _ = runtime.String
var _ = utilities.NewDoubleArray
`))

	handlerTemplate = template.Must(template.New("handler").Parse(`
{{if and .Method.GetClientStreaming .Method.GetServerStreaming}}
{{template "bidi-streaming-request-func" .}}
{{else if .Method.GetClientStreaming}}
{{template "client-streaming-request-func" .}}
{{else}}
{{template "client-rpc-request-func" .}}
{{end}}
`))

	_ = template.Must(handlerTemplate.New("request-func-signature").Parse(strings.Replace(`
{{if .Method.GetServerStreaming}}
func request_{{.Method.Service.Name}}_{{.Method.GetTargetSvrName}}_{{.Method.GetName}}_{{.Index}}(ctx context.Context, marshaler runtime.Marshaler, client {{.Method.GetTargetSvrPackage}}{{.Method.GetTargetSvrName}}Client, req *http.Request, pathParams map[string]string) ({{.Method.GetTargetSvrName}}_{{.Method.GetName}}Client, runtime.ServerMetadata, error)
{{else}}
func request_{{.Method.Service.Name}}_{{.Method.GetTargetSvrName}}_{{.Method.GetName}}_{{.Index}}(ctx context.Context, marshaler runtime.Marshaler, client {{.Method.GetTargetSvrPackage}}{{.Method.GetTargetSvrName}}Client, req *http.Request, pathParams map[string]string) (proto.Message, runtime.ServerMetadata, error)
{{end}}`, "\n", "", -1)))

	_ = template.Must(handlerTemplate.New("client-streaming-request-func").Parse(`
{{template "request-func-signature" .}} {
	var metadata runtime.ServerMetadata
	stream, err := client.{{.Method.GetName}}(ctx)
	if err != nil {
		grpclog.Infof("Failed to start streaming: %v", err)
		return nil, metadata, err
	}
	dec := marshaler.NewDecoder(req.Body)
	for {
		var protoReq {{.Method.RequestType.GoType .Method.Service.File.GoPkg.Path}}
		err = dec.Decode(&protoReq)
		if err == io.EOF {
			break
		}
		if err != nil {
			grpclog.Infof("Failed to decode request: %v", err)
			return nil, metadata, status.Errorf(codes.InvalidArgument, "%v", err)
		}
		if err = stream.Send(&protoReq); err != nil {
			if err == io.EOF {
				break
			}
			grpclog.Infof("Failed to send request: %v", err)
			return nil, metadata, err
		}
	}

	if err := stream.CloseSend(); err != nil {
		grpclog.Infof("Failed to terminate client stream: %v", err)
		return nil, metadata, err
	}
	header, err := stream.Header()
	if err != nil {
		grpclog.Infof("Failed to get header from client: %v", err)
		return nil, metadata, err
	}
	metadata.HeaderMD = header
{{if .Method.GetServerStreaming}}
	return stream, metadata, nil
{{else}}
	msg, err := stream.CloseAndRecv()
	metadata.TrailerMD = stream.Trailer()
	return msg, metadata, err
{{end}}
}
`))

	_ = template.Must(handlerTemplate.New("client-rpc-request-func").Parse(`
{{$AllowPatchFeature := .AllowPatchFeature}}
{{if .HasQueryParam}}
var (
	filter_{{.Method.Service.GetName}}_{{.Method.GetName}}_{{.Index}} = {{.QueryParamFilter}}
)
{{end}}
{{template "request-func-signature" .}} {
	var protoReq {{.Method.RequestType.GoType .Method.Service.File.GoPkg.Path}}
	var metadata runtime.ServerMetadata
{{if .Body}}
	newReader, berr := utilities.IOReaderFactory(req.Body)
	if berr != nil {
		return nil, metadata, status.Errorf(codes.InvalidArgument, "%v", berr)
	}
	if err := marshaler.NewDecoder(newReader()).Decode(&{{.Body.AssignableExpr "protoReq"}}); err != nil && err != io.EOF  {
		return nil, metadata, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	{{- if and $AllowPatchFeature (and (eq (.HTTPMethod) "PATCH") (.FieldMaskField))}}
	if protoReq.{{.FieldMaskField}} != nil && len(protoReq.{{.FieldMaskField}}.GetPaths()) > 0 {
		runtime.CamelCaseFieldMask(protoReq.{{.FieldMaskField}})
	} {{if not (eq "*" .GetBodyFieldPath)}} else {
			if fieldMask, err := runtime.FieldMaskFromRequestBody(newReader()); err != nil {
				return nil, metadata, status.Errorf(codes.InvalidArgument, "%v", err)
			} else {
				protoReq.{{.FieldMaskField}} = fieldMask
			}
	} {{end}}
	{{end}}
{{end}}
{{if .PathParams}}
	var (
		val string
{{- if .HasEnumPathParam}}
		e int32
{{- end}}
{{- if .HasRepeatedEnumPathParam}}
		es []int32
{{- end}}
		ok bool
		err error
		_ = err
	)
	{{$binding := .}}
	{{range $param := .PathParams}}
	{{$enum := $binding.LookupEnum $param}}
	val, ok = pathParams[{{$param | printf "%q"}}]
	if !ok {
		return nil, metadata, status.Errorf(codes.InvalidArgument, "missing parameter %s", {{$param | printf "%q"}})
	}
{{if $param.IsNestedProto3}}
	err = runtime.PopulateFieldFromPath(&protoReq, {{$param | printf "%q"}}, val)
{{else if $enum}}
	e{{if $param.IsRepeated}}s{{end}}, err = {{$param.ConvertFuncExpr}}(val{{if $param.IsRepeated}}, {{$binding.Registry.GetRepeatedPathParamSeparator | printf "%c" | printf "%q"}}{{end}}, {{$enum.GoType $param.Target.Message.File.GoPkg.Path}}_value)
{{else}}
	{{$param.AssignableExpr "protoReq"}}, err = {{$param.ConvertFuncExpr}}(val{{if $param.IsRepeated}}, {{$binding.Registry.GetRepeatedPathParamSeparator | printf "%c" | printf "%q"}}{{end}})
{{end}}
	if err != nil {
		return nil, metadata, status.Errorf(codes.InvalidArgument, "type mismatch, parameter: %s, error: %v", {{$param | printf "%q"}}, err)
	}
{{if and $enum $param.IsRepeated}}
	s := make([]{{$enum.GoType $param.Target.Message.File.GoPkg.Path}}, len(es))
	for i, v := range es {
		s[i] = {{$enum.GoType $param.Target.Message.File.GoPkg.Path}}(v)
	}
	{{$param.AssignableExpr "protoReq"}} = s
{{else if $enum}}
	{{$param.AssignableExpr "protoReq"}} = {{$enum.GoType $param.Target.Message.File.GoPkg.Path}}(e)
{{end}}
	{{end}}
{{end}}
{{if .HasQueryParam}}
	if err := req.ParseForm(); err != nil {
		return nil, metadata, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	if err := runtime.PopulateQueryParameters(&protoReq, req.Form, filter_{{.Method.Service.GetName}}_{{.Method.GetName}}_{{.Index}}); err != nil {
		return nil, metadata, status.Errorf(codes.InvalidArgument, "%v", err)
	}
{{end}}
{{if .Method.GetServerStreaming}}
	stream, err := client.{{.Method.GetName}}(ctx, &protoReq)
	if err != nil {
		return nil, metadata, err
	}
	header, err := stream.Header()
	if err != nil {
		return nil, metadata, err
	}
	metadata.HeaderMD = header
	return stream, metadata, nil
{{else}}
	msg, err := client.{{.Method.GetName}}(ctx, &protoReq, grpc.Header(&metadata.HeaderMD), grpc.Trailer(&metadata.TrailerMD))
	return msg, metadata, err
{{end}}
}`))

	_ = template.Must(handlerTemplate.New("bidi-streaming-request-func").Parse(`
{{template "request-func-signature" .}} {
	var metadata runtime.ServerMetadata
	stream, err := client.{{.Method.GetName}}(ctx)
	if err != nil {
		grpclog.Infof("Failed to start streaming: %v", err)
		return nil, metadata, err
	}
	dec := marshaler.NewDecoder(req.Body)
	handleSend := func() error {
		var protoReq {{.Method.RequestType.GoType .Method.Service.File.GoPkg.Path}}
		err := dec.Decode(&protoReq)
		if err == io.EOF {
			return err
		}
		if err != nil {
			grpclog.Infof("Failed to decode request: %v", err)
			return err
		}
		if err := stream.Send(&protoReq); err != nil {
			grpclog.Infof("Failed to send request: %v", err)
			return err
		}
		return nil
	}
	if err := handleSend(); err != nil {
		if cerr := stream.CloseSend(); cerr != nil {
			grpclog.Infof("Failed to terminate client stream: %v", cerr)
		}
		if err == io.EOF {
			return stream, metadata, nil
		}
		return nil, metadata, err
	}
	go func() {
		for {
			if err := handleSend(); err != nil {
				break
			}
		}
		if err := stream.CloseSend(); err != nil {
			grpclog.Infof("Failed to terminate client stream: %v", err)
		}
	}()
	header, err := stream.Header()
	if err != nil {
		grpclog.Infof("Failed to get header from client: %v", err)
		return nil, metadata, err
	}
	metadata.HeaderMD = header
	return stream, metadata, nil
}
`))

	trailerTemplate = template.Must(template.New("trailer").Parse(`
// type HttpPrehandler func(string, *http.Request) (int, bool)
// type HttpDoneHandler func(string, proto.Message, http.ResponseWriter, *http.Request)
// type QpsHandler func(time.Duration)

{{$UseRequestContext := .UseRequestContext}}
{{range $svc := .Services}}

// Register{{$svc.GetName}}{{$.RegisterFuncSuffix}}Client registers the http handlers for service {{$svc.GetName}}
// to "mux". The handlers forward requests to the grpc endpoint over the given implementation of "{{$svc.GetName}}Client".
// Note: the gRPC framework executes interceptors within the gRPC handler. If the passed in "{{$svc.GetName}}Client"
// doesn't go through the normal gRPC flow (creating a gRPC client etc.) then it will be up to the passed in
// "{@target}Client" to call the correct interceptors.
// @param getEndpoint: a callback func( 'package.Service/Method' ) 'endpoint address'
// @param endCallback: a callback when grpc end, then callback('package.Service/Method' string, reply proto.Message) bool[false:quit]
// @param getClientConn: a callback func( 'package.Service/Method' )(conn, error) to get long connection by method
func Register{{$svc.GetName}}{{$.RegisterFuncSuffix}}Client(
	ctx context.Context, 
	mux *runtime.ServeMux, 
	opts []grpc.DialOption, 
	getEndpoint func(string)string, 
	getClientConn func(string) (*grpc.ClientConn, func(), error),
	beginHandler func(string, *http.Request) (int, bool), 
	doneHandler func(string, proto.Message, http.ResponseWriter, *http.Request), 
	qpsHandler func(time.Duration)) error {

	makeConn := func(meth string) (*grpc.ClientConn, func(), error) {
		if getClientConn != nil {
			return getClientConn(meth)
		}
		addr := getEndpoint(meth)
		conn, err := grpc.Dial(addr, opts...)
		return conn, func() { conn.Close() }, err
	}

	{{range $m := $svc.Methods}}
	{{range $b := $m.Bindings}}
	// 注册{{$m.GetTargetSvrName}}/{{$m.Name}}传输方法入口
	{{if $m.Comment}}{{$m.GetFormatComment}}{{end}}
	mux.Handle({{$b.HTTPMethod | printf "%q"}}, pattern_{{$m.Service.Name}}_{{$m.GetTargetSvrName}}_{{$m.GetName}}_{{$b.Index}}, func(w http.ResponseWriter, req *http.Request, pathParams map[string]string) {
		begin := time.Now()
		defer qpsHandler(time.Since(begin))
		
	{{- if $UseRequestContext }}
		ctx, cancel := context.WithCancel(req.Context())
	{{- else -}}
		ctx, cancel := context.WithCancel(ctx)
	{{- end }}
		defer cancel()
		inboundMarshaler, outboundMarshaler := runtime.MarshalerForRequest(mux, req)

		meth := "{{$svc.File.GoPkg.Name}}.{{$m.GetTargetSvrName}}/{{$m.Name}}"
		if code, ok := beginHandler(meth, req); code == http.StatusAccepted {
			if !ok {
				runtime.HTTPError(ctx, mux, outboundMarshaler, w, req, errors.New("not login yet"))
				return
			}
		} else {
			runtime.OtherErrorHandler(w, req, http.StatusText(code), code)
			return
		}
		
		rctx, err := runtime.AnnotateContext(ctx, mux, req)
		if err != nil {
			runtime.HTTPError(ctx, mux, outboundMarshaler, w, req, err)
			return
		}
		
		conn, closeFunc, err := makeConn(meth)
		if err != nil {
			runtime.HTTPError(ctx, mux, outboundMarshaler, w, req, err)
			return
		}
		defer closeFunc()

		client := {{$m.GetTargetSvrPackage}}New{{$m.GetTargetSvrName}}Client(conn)

		resp, md, err := request_{{$m.Service.Name}}_{{$m.GetTargetSvrName}}_{{$m.GetName}}_{{$b.Index}}(rctx, inboundMarshaler, client, req, pathParams)
		ctx = runtime.NewServerMetadataContext(ctx, md)
		if err != nil {
			runtime.HTTPError(ctx, mux, outboundMarshaler, w, req, err)
			return
		}
		doneHandler(meth, resp, w, req)

		{{if $m.GetServerStreaming}}
		forward_{{$m.Service.Name}}_{{$m.GetTargetSvrName}}_{{$m.GetName}}_{{$b.Index}}(ctx, mux, outboundMarshaler, w, req, func() (proto.Message, error) { return resp.Recv() }, mux.GetForwardResponseOptions()...)
		{{else}}
		{{ if $b.ResponseBody }}
		forward_{{$m.Service.Name}}_{{$m.GetTargetSvrName}}_{{$m.GetName}}_{{$b.Index}}(ctx, mux, outboundMarshaler, w, req, response_{{$svc.GetName}}_{{$m.GetName}}_{{$b.Index}}{resp}, mux.GetForwardResponseOptions()...)
		{{ else }}
		forward_{{$m.Service.Name}}_{{$m.GetTargetSvrName}}_{{$m.GetName}}_{{$b.Index}}(ctx, mux, outboundMarshaler, w, req, resp, mux.GetForwardResponseOptions()...)
		{{end}}
		{{end}}
	})
	{{end}}
	{{end}}
	return nil
}

{{range $m := $svc.Methods}}
{{range $b := $m.Bindings}}
{{if $b.ResponseBody}}
type response_{{$svc.GetName}}_{{$m.GetName}}_{{$b.Index}} struct {
	proto.Message
}

func (m response_{{$svc.GetName}}_{{$m.GetName}}_{{$b.Index}}) XXX_ResponseBody() interface{} {
	response := m.Message.(*{{$m.ResponseType.GoType $m.Service.File.GoPkg.Path}})
	return {{$b.ResponseBody.AssignableExpr "response"}}
}
{{end}}
{{end}}
{{end}}

var (
	{{range $m := $svc.Methods}}
	{{range $b := $m.Bindings}}
	pattern_{{$m.Service.Name}}_{{$m.GetTargetSvrName}}_{{$m.GetName}}_{{$b.Index}} = runtime.MustPattern(runtime.NewPattern({{$b.PathTmpl.Version}}, {{$b.PathTmpl.OpCodes | printf "%#v"}}, {{$b.PathTmpl.Pool | printf "%#v"}}, {{$b.PathTmpl.Verb | printf "%q"}}, runtime.AssumeColonVerbOpt({{$.AssumeColonVerb}})))
	{{end}}
	{{end}}
)

var (
	{{range $m := $svc.Methods}}
	{{range $b := $m.Bindings}}
	forward_{{$m.Service.Name}}_{{$m.GetTargetSvrName}}_{{$m.GetName}}_{{$b.Index}} = {{if $m.GetServerStreaming}}runtime.ForwardResponseStream{{else}}runtime.ForwardResponseMessage{{end}}
	{{end}}
	{{end}}
)
{{end}}`))
)
