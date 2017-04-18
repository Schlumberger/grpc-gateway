package gengatewayclient

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/golang/glog"
	pbdescriptor "github.com/golang/protobuf/protoc-gen-go/descriptor"
	"github.com/grpc-ecosystem/grpc-gateway/protoc-gen-grpc-gateway/descriptor"
	"github.com/grpc-ecosystem/grpc-gateway/utilities"
)

type param struct {
	*descriptor.File
	Imports           []descriptor.GoPackage
	UseRequestContext bool
	reg               *descriptor.Registry
}

type binding struct {
	*descriptor.Binding
	QueryParams []descriptor.Parameter
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
	Services          []*descriptor.Service
	UseRequestContext bool
}

func applyTemplate(p param) (string, error) {
	w := bytes.NewBuffer(nil)
	if err := headerTemplate.Execute(w, p); err != nil {
		return "", err
	}

	var targetServices []*descriptor.Service
	for _, svc := range p.Services {
		var methodWithBindingsSeen bool
		for _, meth := range svc.Methods {
			glog.V(2).Infof("Processing %s.%s", svc.GetName(), meth.GetName())
			methName := strings.Title(*meth.Name)
			meth.Name = &methName
			for _, b := range meth.Bindings {
				methodWithBindingsSeen = true

				var queryParams []descriptor.Parameter
				var err error

				if b.HTTPMethod == "GET" {
					// build up the list of query params
					queryParams, err = messageToQueryParameters(b.Method.RequestType, p.reg, b.PathParams)
					if err != nil {
						return "", err
					}
				}

				if err := clientFuncTemplate.Execute(w, binding{Binding: b, QueryParams: queryParams}); err != nil {
					return "", err
				}

				// if err := handlerTemplate.Execute(w, binding{Binding: b}); err != nil {
				// 	return "", err
				// }
			}
		}
		if methodWithBindingsSeen {
			targetServices = append(targetServices, svc)
		}
	}
	if len(targetServices) == 0 {
		return "", errNoTargetService
	}

	tp := trailerParams{
		Services:          targetServices,
		UseRequestContext: p.UseRequestContext,
	}

	if err := clientStructTemplate.Execute(w, tp); err != nil {
		return "", err
	}

	if err := utilsFuncTemplate.Execute(w, tp); err != nil {
		return "", err
	}

	// if err := trailerTemplate.Execute(w, tp); err != nil {
	// 	return "", err
	// }
	return w.String(), nil
}

// messageToQueryParameters converts a message to a list of swagger query parameters.
func messageToQueryParameters(message *descriptor.Message, reg *descriptor.Registry, pathParams []descriptor.Parameter) (params []descriptor.Parameter, err error) {
	for _, field := range message.Fields {

		p, err := queryParams(message, field, []descriptor.FieldPathComponent{}, reg, pathParams)
		if err != nil {
			return nil, err
		}
		params = append(params, p...)
	}
	return params, nil
}

// queryParams converts a field to a list of swagger query parameters recuresively.
func queryParams(message *descriptor.Message, field *descriptor.Field, fieldPath []descriptor.FieldPathComponent, reg *descriptor.Registry, pathParams []descriptor.Parameter) (params []descriptor.Parameter, err error) {
	// make sure the parameter is not already listed as a path parameter
	for _, pathParam := range pathParams {
		if pathParam.Target == field {
			return nil, nil
		}
	}

	newFieldPath := append(fieldPath, descriptor.FieldPathComponent{
		Name:   field.GetName(),
		Target: field,
	})

	if field.FieldDescriptorProto.GetType() == pbdescriptor.FieldDescriptorProto_TYPE_MESSAGE {
		//need to apply the queryParam recursively
		msg, err := reg.LookupMsg("", field.GetTypeName())
		if err != nil {
			return nil, fmt.Errorf("unknown message type %s", field.GetTypeName())
		}
		for _, nestedField := range msg.Fields {

			p, err := queryParams(msg, nestedField, newFieldPath, reg, pathParams)
			if err != nil {
				return nil, err
			}
			params = append(params, p...)
		}
	} else {

		params = []descriptor.Parameter{descriptor.Parameter{
			Target:    field,
			FieldPath: newFieldPath},
		}
	}

	return params, nil
}

func getJsonName(param descriptor.Parameter) string {
	var components []string
	for _, c := range param.FieldPath {
		components = append(components, c.Target.GetJsonName())
	}
	return strings.Join(components, ".")
}

var (
	funcMap = template.FuncMap{
		"ToJsonName": getJsonName,
	}

	headerTemplate = template.Must(template.New("header").Parse(`
// Code generated by protoc-gen-grpc-gateway-client
// source: {{.GetName}}
// DO NOT EDIT!
//
// Not fully implemented, work still in progress
// (Vincent Rondot)




package {{.GoPkg.Name}}
import (
	{{range $i := .Imports}}{{if $i.Standard}}{{$i | printf "%s\n"}}{{end}}{{end}}

	{{range $i := .Imports}}{{if not $i.Standard}}{{$i | printf "%s\n"}}{{end}}{{end}}
)


var _ = fmt.Print
var _ = strings.Compare

`))

	clientFuncTemplate = template.Must(handlerTemplate.New("client-func").Funcs(funcMap).Parse(`
{{if .Method.GetServerStreaming}}
//{{.Method.Service.GetName}}.{{.Method.GetName}} not implemented
{{else}}
func (c* default{{.Method.Service.GetName}}HttpClient) {{.Method.GetName}}(ctx context.Context, in *{{.Method.RequestType.GoType .Method.Service.File.GoPkg.Path}}) (*{{.Method.ResponseType.GoType .Method.Service.File.GoPkg.Path}}, error) {
	var (
		resp {{.Method.ResponseType.GoType .Method.Service.File.GoPkg.Path}}
		val string
		body proto.Message = nil
	)
	_ = val


	// create path and map variables	
	localVarPath := c.Url + "{{.PathTmpl.Template}}"
	{{range $param := .PathParams}}
		val = fmt.Sprintf("%v", {{$param.RHS "in"}})
		localVarPath = strings.Replace(localVarPath, "{"+"{{$param}}"+"}", fmt.Sprintf("%v", val), -1)
	{{end}}


	// Query params
	v := url.Values{}
	{{range $param := .QueryParams}}
		v.Set("{{$param | ToJsonName | }}", {{$param.RHS "in"}})
	{{end}}
		
	localVarPath = localVarPath+"?" + v.Encode()

	// Body
	{{if .Body}}
		body = {{.Body.RHS "in"}}
	{{else}}
		{{if ne .HTTPMethod "GET"}}
			//Not a GET, and no body define: we set the whole message as body
			body = in
		{{end}}
	{{end}}
	



	r, err := c.makeRequest("{{.HTTPMethod}}", localVarPath, body, ctx)
	if err != nil {
		return &resp, err
	}
	err = c.processResponseEntity(r, &resp, 200)

	return &resp, err


}

{{end}}
`))

	clientStructTemplate = template.Must(handlerTemplate.New("client-struct").Parse(`
	//TODO: make interface, and NewConstructor...
	//Also, we could create a confgurable stub...


	{{range $svc := .Services}}
		type {{$svc.GetName}}HttpClient interface {
			{{range $m := $svc.Methods}}
				{{$m.GetName}}(ctx context.Context, in *{{$m.RequestType.GoType $m.Service.File.GoPkg.Path}}) (*{{$m.ResponseType.GoType $m.Service.File.GoPkg.Path}}, error)
			{{end}}

			//Only here temporarily. Would need to be removed...
			//getting the Token should be part of the AuthManager, and should not have "google" reference in the interface definition... maybe "GetAuthentication"... ?
			GetGoogleAccessToken() (string, error)
		}


		type default{{$svc.GetName}}HttpClient struct {
			Url string
		}



		func New{{$svc.GetName}}HttpClient(url string)  {{$svc.GetName}}HttpClient {
			return &default{{$svc.GetName}}HttpClient{url}
		}




		type {{$svc.GetName}}HttpClientStub struct {
			{{range $m := $svc.Methods}}
				{{$m.GetName}}Stub func(ctx context.Context, in *{{$m.RequestType.GoType $m.Service.File.GoPkg.Path}}) (*{{$m.ResponseType.GoType $m.Service.File.GoPkg.Path}}, error)
			{{end}}

			
		}

		{{range $m := $svc.Methods}}
			func (c *{{$svc.GetName}}HttpClientStub) {{$m.GetName}}(ctx context.Context, in *{{$m.RequestType.GoType $m.Service.File.GoPkg.Path}}) (*{{$m.ResponseType.GoType $m.Service.File.GoPkg.Path}}, error) {
				return c.{{$m.GetName}}Stub(ctx, in)
			}
		{{end}}

		func (c *{{$svc.GetName}}HttpClientStub) GetGoogleAccessToken() (string, error) {
			return "123456",nil
		}

		
	{{end}}
`))

	utilsFuncTemplate = template.Must(handlerTemplate.New("utils-func").Parse(`
		//TODO: we should maybe put that in some shared utility package...

	{{range $svc := .Services}}

		func (c* default{{$svc.GetName}}HttpClient) makeRequest(method string, url string, m proto.Message, ctx context.Context) (*http.Response, error) {
			req, err := c.buildRequest(method, url, m, ctx)
			if err != nil {
				return nil, err
			}
			return http.DefaultClient.Do(req)
		}

		func (c* default{{$svc.GetName}}HttpClient) buildRequest(method string, url string, m proto.Message, ctx context.Context) (*http.Request, error) {
			body, err := c.encodeEntity(m)
			if err != nil {
				return nil, err
			}

			req, err := http.NewRequest(method, url, body)
			if err != nil {
				return req, err
			}
			req.Header.Set("content-type", "application/json")

			if(ctx != nil) {
				// retrieve metadata from context
				md, ok := metadata.FromOutgoingContext(ctx)
				if !ok {
					return nil, grpc.Errorf(codes.Internal, "Unable to get metadata from context")
				}
				authHeader := md["authorization"]

				for _, v := range authHeader {
					req.Header.Set("Authorization", v)
				}
			}
			

			return req, err
		}

		func (c* default{{$svc.GetName}}HttpClient) encodeEntity(m proto.Message) (io.Reader, error) {
			if m == nil {
				return nil, nil
			} else {
				marshal := jsonpb.Marshaler{}
				s, err := marshal.MarshalToString(m)
				if err != nil {
					return nil, err
				}
				return bytes.NewBuffer([]byte(s)), nil
			}
		}

		func (c* default{{$svc.GetName}}HttpClient) processResponseEntity(r *http.Response, m proto.Message, expectedStatus int) error {
			if err := c.processResponse(r, expectedStatus); err != nil {
				return err
			}

			respBody, err := ioutil.ReadAll(r.Body)
			if err != nil {
				return err
			}

			if err = jsonpb.UnmarshalString(string(respBody), m); err != nil {
				return err
			}

			return nil
		}
		func (c* default{{$svc.GetName}}HttpClient) processResponse(r *http.Response, expectedStatus int) error {
			if r.StatusCode != expectedStatus {
				return errors.New("response status of " + r.Status)
			}

			return nil
		}

		func (c* default{{$svc.GetName}}HttpClient) GetGoogleAccessToken() (string, error) {
			ctx := context.Background()
			tokenSource, err := google.DefaultTokenSource(ctx, "email")
			if err != nil {
				return "", err
			}
			token, err := tokenSource.Token()
			if err != nil {
				return "", err
			}
			return token.AccessToken, nil
		}
{{end}}
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
func request_{{.Method.Service.GetName}}_{{.Method.GetName}}_{{.Index}}(ctx context.Context, marshaler runtime.Marshaler, client {{.Method.Service.GetName}}Client, req *http.Request, pathParams map[string]string) ({{.Method.Service.GetName}}_{{.Method.GetName}}Client, runtime.ServerMetadata, error)
{{else}}
func request_{{.Method.Service.GetName}}_{{.Method.GetName}}_{{.Index}}(ctx context.Context, marshaler runtime.Marshaler, client {{.Method.Service.GetName}}Client, req *http.Request, pathParams map[string]string) (proto.Message, runtime.ServerMetadata, error)
{{end}}`, "\n", "", -1)))

	_ = template.Must(handlerTemplate.New("client-streaming-request-func").Parse(`
{{template "request-func-signature" .}} {
	var metadata runtime.ServerMetadata
	stream, err := client.{{.Method.GetName}}(ctx)
	if err != nil {
		grpclog.Printf("Failed to start streaming: %v", err)
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
			grpclog.Printf("Failed to decode request: %v", err)
			return nil, metadata, grpc.Errorf(codes.InvalidArgument, "%v", err)
		}
		if err = stream.Send(&protoReq); err != nil {
			grpclog.Printf("Failed to send request: %v", err)
			return nil, metadata, err
		}
	}

	if err := stream.CloseSend(); err != nil {
		grpclog.Printf("Failed to terminate client stream: %v", err)
		return nil, metadata, err
	}
	header, err := stream.Header()
	if err != nil {
		grpclog.Printf("Failed to get header from client: %v", err)
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
{{if .HasQueryParam}}
var (
	filter_{{.Method.Service.GetName}}_{{.Method.GetName}}_{{.Index}} = {{.QueryParamFilter}}
)
{{end}}
{{template "request-func-signature" .}} {
	var protoReq {{.Method.RequestType.GoType .Method.Service.File.GoPkg.Path}}
	var metadata runtime.ServerMetadata
{{if .Body}}
	if err := marshaler.NewDecoder(req.Body).Decode(&{{.Body.RHS "protoReq"}}); err != nil {
		return nil, metadata, grpc.Errorf(codes.InvalidArgument, "%v", err)
	}
{{end}}
{{if .PathParams}}
	var (
		val string
		ok bool
		err error
		_ = err
	)
	{{range $param := .PathParams}}
	val, ok = pathParams[{{$param | printf "%q"}}]
	if !ok {
		return nil, metadata, grpc.Errorf(codes.InvalidArgument, "missing parameter %s", {{$param | printf "%q"}})
	}
{{if $param.IsNestedProto3 }}
	err = runtime.PopulateFieldFromPath(&protoReq, {{$param | printf "%q"}}, val)
{{else}}
	{{$param.RHS "protoReq"}}, err = {{$param.ConvertFuncExpr}}(val)
{{end}}
	if err != nil {
		return nil, metadata, err
	}
	{{end}}
{{end}}
{{if .HasQueryParam}}
	if err := runtime.PopulateQueryParameters(&protoReq, req.URL.Query(), filter_{{.Method.Service.GetName}}_{{.Method.GetName}}_{{.Index}}); err != nil {
		return nil, metadata, grpc.Errorf(codes.InvalidArgument, "%v", err)
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
		grpclog.Printf("Failed to start streaming: %v", err)
		return nil, metadata, err
	}
	dec := marshaler.NewDecoder(req.Body)
	handleSend := func() error {
		var protoReq {{.Method.RequestType.GoType .Method.Service.File.GoPkg.Path}}
		err = dec.Decode(&protoReq)
		if err == io.EOF {
			return err
		}
		if err != nil {
			grpclog.Printf("Failed to decode request: %v", err)
			return err
		}
		if err = stream.Send(&protoReq); err != nil {
			grpclog.Printf("Failed to send request: %v", err)
			return err
		}
		return nil
	}
	if err := handleSend(); err != nil {
		if cerr := stream.CloseSend(); cerr != nil {
			grpclog.Printf("Failed to terminate client stream: %v", cerr)
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
			grpclog.Printf("Failed to terminate client stream: %v", err)
		}
	}()
	header, err := stream.Header()
	if err != nil {
		grpclog.Printf("Failed to get header from client: %v", err)
		return nil, metadata, err
	}
	metadata.HeaderMD = header
	return stream, metadata, nil
}
`))

	trailerTemplate = template.Must(template.New("trailer").Parse(`
{{$UseRequestContext := .UseRequestContext}}
{{range $svc := .Services}}
// Register{{$svc.GetName}}HandlerFromEndpoint is same as Register{{$svc.GetName}}Handler but
// automatically dials to "endpoint" and closes the connection when "ctx" gets done.
func Register{{$svc.GetName}}HandlerFromEndpoint(ctx context.Context, mux *runtime.ServeMux, endpoint string, opts []grpc.DialOption) (err error) {
	conn, err := grpc.Dial(endpoint, opts...)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			if cerr := conn.Close(); cerr != nil {
				grpclog.Printf("Failed to close conn to %s: %v", endpoint, cerr)
			}
			return
		}
		go func() {
			<-ctx.Done()
			if cerr := conn.Close(); cerr != nil {
				grpclog.Printf("Failed to close conn to %s: %v", endpoint, cerr)
			}
		}()
	}()

	return Register{{$svc.GetName}}Handler(ctx, mux, conn)
}

// Register{{$svc.GetName}}Handler registers the http handlers for service {{$svc.GetName}} to "mux".
// The handlers forward requests to the grpc endpoint over "conn".
func Register{{$svc.GetName}}Handler(ctx context.Context, mux *runtime.ServeMux, conn *grpc.ClientConn) error {
	client := New{{$svc.GetName}}Client(conn)
	{{range $m := $svc.Methods}}
	{{range $b := $m.Bindings}}
	mux.Handle({{$b.HTTPMethod | printf "%q"}}, pattern_{{$svc.GetName}}_{{$m.GetName}}_{{$b.Index}}, func(w http.ResponseWriter, req *http.Request, pathParams map[string]string) {
	{{- if $UseRequestContext }}
		ctx, cancel := context.WithCancel(req.Context())
	{{- else -}}
		ctx, cancel := context.WithCancel(ctx)
	{{- end }}
		defer cancel()
		if cn, ok := w.(http.CloseNotifier); ok {
			go func(done <-chan struct{}, closed <-chan bool) {
				select {
				case <-done:
				case <-closed:
					cancel()
				}
			}(ctx.Done(), cn.CloseNotify())
		}
		inboundMarshaler, outboundMarshaler := runtime.MarshalerForRequest(mux, req)
		rctx, err := runtime.AnnotateContext(ctx, req)
		if err != nil {
			runtime.HTTPError(ctx, outboundMarshaler, w, req, err)
		}
		resp, md, err := request_{{$svc.GetName}}_{{$m.GetName}}_{{$b.Index}}(rctx, inboundMarshaler, client, req, pathParams)
		ctx = runtime.NewServerMetadataContext(ctx, md)
		if err != nil {
			runtime.HTTPError(ctx, outboundMarshaler, w, req, err)
			return
		}
		{{if $m.GetServerStreaming}}
		forward_{{$svc.GetName}}_{{$m.GetName}}_{{$b.Index}}(ctx, outboundMarshaler, w, req, func() (proto.Message, error) { return resp.Recv() }, mux.GetForwardResponseOptions()...)
		{{else}}
		forward_{{$svc.GetName}}_{{$m.GetName}}_{{$b.Index}}(ctx, outboundMarshaler, w, req, resp, mux.GetForwardResponseOptions()...)
		{{end}}
	})
	{{end}}
	{{end}}
	return nil
}

var (
	{{range $m := $svc.Methods}}
	{{range $b := $m.Bindings}}
	pattern_{{$svc.GetName}}_{{$m.GetName}}_{{$b.Index}} = runtime.MustPattern(runtime.NewPattern({{$b.PathTmpl.Version}}, {{$b.PathTmpl.OpCodes | printf "%#v"}}, {{$b.PathTmpl.Pool | printf "%#v"}}, {{$b.PathTmpl.Verb | printf "%q"}}))
	{{end}}
	{{end}}
)

var (
	{{range $m := $svc.Methods}}
	{{range $b := $m.Bindings}}
	forward_{{$svc.GetName}}_{{$m.GetName}}_{{$b.Index}} = {{if $m.GetServerStreaming}}runtime.ForwardResponseStream{{else}}runtime.ForwardResponseMessage{{end}}
	{{end}}
	{{end}}
)
{{end}}`))
)
