package gengatewayclient

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/golang/glog"
	pbdescriptor "github.com/golang/protobuf/protoc-gen-go/descriptor"
	"github.com/grpc-ecosystem/grpc-gateway/protoc-gen-grpc-gateway/descriptor"
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

type trailerParams struct {
	Services          []*descriptor.Service
	UseRequestContext bool
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

			//We only care about the 1st binding.
			if len(meth.Bindings) > 0 {
				b := meth.Bindings[0]
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

	if err := utilsFuncTemplate.Execute(w, tp); err != nil {
		return "", err
	}

	if err := clientInterfaceTemplate.Execute(w, tp); err != nil {
		return "", err
	}

	if err := clientStructsTemplate.Execute(w, tp); err != nil {
		return "", err
	}

	if err := clientStubFuncsTemplate.Execute(w, tp); err != nil {
		return "", err
	}

	return w.String(), nil
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
// This was copied/mimicked from the following:
// - protoc-gen-grpc-gatewat : the use of go template the generate the gile
// - protoc-gen-swagger : to generate the query parameter (functions messageToQueryParameters and queryParams is were they were inspired from - copied & adapted)
//
// (Vincent Rondot)




package {{.GoPkg.Name}}
import (
	{{range $i := .Imports}}{{if $i.Standard}}{{$i | printf "%s\n"}}{{end}}{{end}}

	{{range $i := .Imports}}{{if not $i.Standard}}{{$i | printf "%s\n"}}{{end}}{{end}}
)


var _ = fmt.Print
var _ = strings.Compare

`))

	clientFuncTemplate = template.Must(template.New("client-func").Funcs(funcMap).Parse(`
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
		v.Set("{{$param | }}", fmt.Sprintf("%v", {{$param.RHS "in"}}))
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

	clientStubFuncsTemplate = template.Must(template.New("client-stub-funcs").Parse(`
		//define the client stubs Functions
		{{range $svc := .Services}}
			{{range $m := $svc.Methods}}
				func (c *{{$svc.GetName}}HttpClientStub) {{$m.GetName}}(ctx context.Context, in *{{$m.RequestType.GoType $m.Service.File.GoPkg.Path}}) (*{{$m.ResponseType.GoType $m.Service.File.GoPkg.Path}}, error) {
					return c.{{$m.GetName}}Stub(ctx, in)
				}
			{{end}}
		{{end}}
`))

	clientInterfaceTemplate = template.Must(template.New("client-interface").Parse(`
	//define the client interface
	{{range $svc := .Services}}
		type {{$svc.GetName}}HttpClient interface {
			{{range $m := $svc.Methods}}
				{{$m.GetName}}(ctx context.Context, in *{{$m.RequestType.GoType $m.Service.File.GoPkg.Path}}) (*{{$m.ResponseType.GoType $m.Service.File.GoPkg.Path}}, error)
			{{end}}
		}


	{{end}}
`))

	clientStructsTemplate = template.Must(template.New("client-structs").Parse(`
	//define the client struct (w/ constructor), as well as the client stub struct
	{{range $svc := .Services}}

		type default{{$svc.GetName}}HttpClient struct {
			Url string
		}

		func New{{$svc.GetName}}HttpClient(url string)  {{$svc.GetName}}HttpClient {
			//remove trailing / if present
			updatedUrl := url
			if strings.HasSuffix(url, "/") {
				updatedUrl = url[:len(url)-len("/")]
			}

			return &default{{$svc.GetName}}HttpClient{updatedUrl}
		}

		type {{$svc.GetName}}HttpClientStub struct {
			{{range $m := $svc.Methods}}
				{{$m.GetName}}Stub func(ctx context.Context, in *{{$m.RequestType.GoType $m.Service.File.GoPkg.Path}}) (*{{$m.ResponseType.GoType $m.Service.File.GoPkg.Path}}, error)
			{{end}}
		}

		func New{{$svc.GetName}}HttpClientStub()  *{{$svc.GetName}}HttpClientStub {
			stub := &{{$svc.GetName}}HttpClientStub{}
	
			{{range $m := $svc.Methods}}
				stub.{{$m.GetName}}Stub = func(ctx context.Context, in *{{$m.RequestType.GoType $m.Service.File.GoPkg.Path}}) (*{{$m.ResponseType.GoType $m.Service.File.GoPkg.Path}}, error) {
					return &{{$m.ResponseType.GoType $m.Service.File.GoPkg.Path}}{}, nil
				}
			{{end}}
	
			return stub
		}


	{{end}}

`))

	utilsFuncTemplate = template.Must(template.New("utils-func").Parse(strings.Replace(`
	//TODO: we could maybe put that in some shared utility package...
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
				if ok {
					authHeader := md["authorization"]

					for _, v := range authHeader {
						req.Header.Set("Authorization", v)
					}
				} else {
					println("no Auth. info provided... doing un-authenticated call ")
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

			jsonMarshaler := new(jsonpb.Unmarshaler)
			jsonMarshaler.AllowUnknownFields = true // This is required so that we can have backward compatibility with a server that is providing new fields... They will just be ignored
			jsonMarshaler.Unmarshal(bytes.NewReader(respBody), m)

			if err = jsonMarshaler.Unmarshal(bytes.NewReader(respBody), m); err != nil {
				return err
			}

			return nil
		}



		


		func (c* default{{$svc.GetName}}HttpClient) processResponse(r *http.Response, expectedStatus int) error {
			if r.StatusCode != expectedStatus {
				//parse body, and create a GRPC error from it
		
				respBody, err := ioutil.ReadAll(r.Body)
				if err != nil {
					return grpc.Errorf(codes.Unknown, "Cannot read error from response")
				}

				type errorBody struct {
					Error string <<SimpleQuote>>protobuf:"bytes,1,name=error" json:"error"<<SimpleQuote>>
					Code  int32  <<SimpleQuote>>protobuf:"varint,2,name=code" json:"code"<<SimpleQuote>>
				}
		
				var errBody errorBody
				err = json.Unmarshal(respBody, &errBody)
				if err != nil {
					return grpc.Errorf(codes.Unknown, string(respBody))
				}
		
				return grpc.Errorf(codes.Code(errBody.Code), errBody.Error)
		
			}

			return nil
		}
{{end}}
`, "<<SimpleQuote>>", "`", -1)))
)
