// Command protoc-gen-grpc-gateway is a plugin for Google protocol buffer
// compiler to generate a reverse-proxy, which converts incoming RESTful
// HTTP/1 requests gRPC invocation.
// You rarely need to run this program directly. Instead, put this program
// into your $PATH with a name "protoc-gen-grpc-gateway" and run
//   protoc --grpc-gateway_out=output_directory path/to/input.proto
//
// See README.md for more details.
package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/golang/glog"
	"github.com/golang/protobuf/proto"
	plugin "github.com/golang/protobuf/protoc-gen-go/plugin"
	"github.com/grpc-ecosystem/grpc-gateway/codegenerator"

	"github.com/generalzgd/protoc-gen-grpc-httpgw/descriptor"
	"github.com/generalzgd/protoc-gen-grpc-httpgw/gengateway"
)

var (
	importPrefix               = flag.String("import_prefix", "", "prefix to be added to go package paths for imported proto files")
	importPath                 = flag.String("import_path", "", "used as the package if no input files declare go_package. If it contains slashes, everything up to the rightmost slash is ignored.")
	registerFuncSuffix         = flag.String("register_func_suffix", "Handler", "used to construct names of generated Register*<Suffix> methods.")
	useRequestContext          = flag.Bool("request_context", true, "determine whether to use http.Request's context or not")
	allowDeleteBody            = flag.Bool("allow_delete_body", false, "unless set, HTTP DELETE methods may not have a body")
	grpcAPIConfiguration       = flag.String("grpc_api_configuration", "", "path to gRPC API Configuration in YAML format")
	pathType                   = flag.String("paths", "", "specifies how the paths of generated files are structured")
	allowRepeatedFieldsInBody  = flag.Bool("allow_repeated_fields_in_body", false, "allows to use repeated field in `body` and `response_body` field of `google.api.http` annotation option")
	repeatedPathParamSeparator = flag.String("repeated_path_param_separator", "csv", "configures how repeated fields should be split. Allowed values are `csv`, `pipes`, `ssv` and `tsv`.")
	allowPatchFeature          = flag.Bool("allow_patch_feature", true, "determines whether to use PATCH feature involving update masks (using google.protobuf.FieldMask).")
	allowColonFinalSegments    = flag.Bool("allow_colon_final_segments", false, "determines whether colons are permitted in the final segment of a path")
	versionFlag                = flag.Bool("version", false, "print the current verison")
	file                       = flag.String("file", "-", "where to load data from")
	//file               = flag.String("file", "./test_in.bts", "where to load data from")
	debug = flag.Bool("debug", false, "")
	definePrefix       = flag.String("define_prefix", "", "var define prefix")
)

// Variables set by goreleaser at build time
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	flag.Parse()
	defer glog.Flush()

	if *versionFlag {
		fmt.Printf("Version %v, commit %v, built at %v\n", version, commit, date)
		os.Exit(0)
	}

	reg := descriptor.NewRegistry()

	glog.V(1).Info("Parsing code generator request")

	f := os.Stdin
	if *file != "-" {
		var err error
		pt := *file
		if runtime.GOOS == "windows" {
			pt = filepath.Join(filepath.Dir(os.Args[0]), strings.Trim(pt, "./"))
		}

		f, err = os.Open(pt)
		if err != nil {
			glog.Fatal(err, "open file err.")
			return
		}
	}

	req, err := codegenerator.ParseRequest(f)
	if err != nil {
		glog.Fatal(err)
	}
	glog.V(1).Info("Parsed code generator request")
	if req.Parameter != nil {
		for _, p := range strings.Split(req.GetParameter(), ",") {
			spec := strings.SplitN(p, "=", 2)
			if len(spec) == 1 {
				if err := flag.CommandLine.Set(spec[0], ""); err != nil {
					glog.Fatalf("Cannot set flag %s", p)
				}
				continue
			}
			name, value := spec[0], spec[1]
			if strings.HasPrefix(name, "M") {
				reg.AddPkgMap(name[1:], value)
				continue
			}
			if err := flag.CommandLine.Set(name, value); err != nil {
				glog.Fatalf("Cannot set flag %s", p)
			}
		}
	}

	g := gengateway.New(reg, *useRequestContext, *registerFuncSuffix, *pathType, *allowPatchFeature)

	if *grpcAPIConfiguration != "" {
		if err := reg.LoadGrpcAPIServiceFromYAML(*grpcAPIConfiguration); err != nil {
			emitError(err)
			return
		}
	}

	reg.SetPrefix(*importPrefix)
	reg.SetImportPath(*importPath)
	reg.SetAllowDeleteBody(*allowDeleteBody)
	reg.SetAllowRepeatedFieldsInBody(*allowRepeatedFieldsInBody)
	reg.SetAllowColonFinalSegments(*allowColonFinalSegments)
	if err := reg.SetRepeatedPathParamSeparator(*repeatedPathParamSeparator); err != nil {
		emitError(err)
		return
	}
	if err := reg.Load(req); err != nil {
		emitError(err)
		return
	}

	var targets []*descriptor.File
	for _, target := range req.FileToGenerate {
		f, err := reg.LookupFile(target)
		if err != nil {
			glog.Fatal(err)
		}
		targets = append(targets, f)
		//
		comments := extractComments(f)
		reg.AddComments(*f.Name, comments)
	}

	if err := reg.ParseCommentsSource(req.ProtoFile); err != nil {
		emitError(err)
	}

	out, err := g.Generate(targets)
	glog.V(1).Info("Processed code generator request")
	if err != nil {
		emitError(err)
		return
	}
	emitFiles(out)
}

func extractComments(file *descriptor.File) map[string]string {
	comments := make(map[string]string)
	for _, loc := range file.GetSourceCodeInfo().GetLocation() {
		if loc.LeadingComments == nil {
			continue
		}
		var t []string
		for _, n := range loc.Path {
			t = append(t, strconv.Itoa(int(n)))
		}
		comments[strings.Join(t, ",")] = strings.TrimSpace(*loc.LeadingComments)
	}
	return comments
}

func emitFiles(out []*plugin.CodeGeneratorResponse_File) {
	emitResp(&plugin.CodeGeneratorResponse{File: out})
}

func emitError(err error) {
	emitResp(&plugin.CodeGeneratorResponse{Error: proto.String(err.Error())})
}

func emitResp(resp *plugin.CodeGeneratorResponse) {
	buf, err := proto.Marshal(resp)
	if err != nil {
		glog.Fatal(err)
	}
	if _, err := os.Stdout.Write(buf); err != nil {
		glog.Fatal(err)
	}

	if *debug {
		ioutil.WriteFile("test_out.pb.httpgw.go", buf, os.ModePerm)
	}
}
