# grpc-httpgw(http网关)代码生成插件

## Useage

### 插件文件

```shell
# 生成文件
go build -o $GOPATH/bin/protoc-gen-grpc-httpgw
```

### 插件参数

```shell
# import_prefix: 导入包的前缀，默认空
# import_path: 导入包的指定目录，默认空（即要导入的包都在同一级目录里）
# file: 指定文件，默认空（由protoc传入），对应的文件要对应CodeGeneratorRequest结构
```

### 使用命令

```shell
# generate http grpc gateway
protoc -Iproto --grpc-httpgw_out=logtostderr=true:./goproto ./proto/imgate.proto
# -Iproto 增加一个导入目录
# --{grpc-httpgw}_out 对应插件文件名 protoc-gen-{grpc-httpgw}
# 对应生成 xxx.pb.httpgw.go
```

## Schema例子

```protobuf
// 后端服务 authorize.proto
service Authorize {
    // 校验用户
    rpc Login (ImLoginRequest) returns (ImLoginReply) {}
}

// 后端服务 im.proto
service Im {
    // 已读
    rpc Read(ImReadRequest) returns (ImReadReply) {}
}

// 网关 imgate.proto
// 定义一个grpc tcp/ws网关
service ImGate {
	// 登录注释
	// @transmit
	// @target Authorize
    rpc Login (ImLoginRequest) returns (ImLoginReply) {
        option (google.api.http) = {
            post: "/v1/imgate/login"
            body: "*"
        };
    }
    
    // 已读
    // @transmit
    // @target Im
    rpc Read(ImReadRequest) returns (ImReadReply) {
        option (google.api.http) = {
            post: "/v1/imgate/read"
            body: "*"
        };
    }
}

// @transmit 识别需要转发的method(rpc)
// @target 目标后端服务名（一定要跟后端的服务名称对上），如果不存在则以当前service名代替（实际运行会有问题）
// 因此，对于该插件必须要有这两个tag，缺一不可
// 调用方法名、参数、返回类型也要跟后端服务的方法名、参数、返回类型对上
```

## 应用代码

```go
// 启动服务
func (p *Manager) serveHttp() error {
	addr := fmt.Sprintf(":%d", 8080)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mux := runtime.NewServeMux()
	opts := []grpc.DialOption{grpc.WithInsecure()}
	// todo 目标服务是否启用tls

	err := zqproto.RegisterImGateHandlerClient(ctx, mux, opts, p.getEndpointByMeth, p.httpCallBeginHandler, p.httpCallDoneHandler)
	if err != nil {
		logs.Error("serve http gate fail.", err)
		return err
	}

	go func() {
		http.ListenAndServe(addr, mux)
	}()
	// p.svrMap[cfg.Name] = server
	logs.Info("start serve %s(%s) with secure(%v)", cfg.Name, addr, cfg.Secure)
	return nil
}

// grpc转发前的回调处理
func (p *Manager) httpCallBeginHandler(meth string, req *http.Request) bool {
    // 校验cookie
	cookie_guid, err := req.Cookie("ZQ_GUID")
	if err != nil || cookie_guid.Value == "" || strings.Index(cookie_guid.Value, ".") < 0 {
		return false
	}
    // 映射到对应cmdid, meth->package.Service/Method，例如：zqproto.Authorize/Login
	cmdid := Method2Cmdid[meth]
	if cmdid == gocmd.ID_ImLoginRequest {
		// 不处理 conninfo
	} else {
		info, ok := p.httpInfoCenter.Get(cookie_guid.Value)
		// 如果未登录，过期，则终止转发
		if !ok || !info.State {
			return false
		}

		if cmdid == gocmd.ID_ImLogoutRequest {
			p.httpInfoCenter.Delete(cookie_guid.Value)
		}
		// 将客户端链接信息转换为grpc的Header信息
		tmp, _ := dcopy.InstanceToMap(info)
		for k, v := range tmp {
			// header key需要有MetadataHeaderPrefix开头，才会识别出来并转发给endpoint; 终端取到的key不含MetadataHeaderPrefix
			req.Header.Add(runtime.MetadataHeaderPrefix+k, libs.Interface2String(v)) // MetadataHeaderPrefix
		}
	}
	return true
}

// grpc结束回调处理
func (p *Manager) httpCallDoneHandler(meth string, reply proto.Message, w http.ResponseWriter, req *http.Request) {
     // 校验cookie
	cookie_guid, err := req.Cookie("ZQ_GUID")
	if err != nil || cookie_guid.Value == "" {
		return
	}
    // 映射到对应cmdid, meth->package.Service/Method，例如：zqproto.Authorize/Login
	cmdid, _ := Method2Cmdid[meth]
	if cmdid == gocmd.ID_ImLoginRequest {
        // 登录成功，保存用户信息
		if data, ok := reply.(*zqproto.ImLoginReply); ok {
			info, ok := p.httpInfoCenter.Get(cookie_guid.Value)
			if !ok {
				info = &common.ClientConnInfo{}
			}
			if data.Code == 0 {
				p.saveUserInfo(info, data, cookie_guid.Value, 3600*24) // 1 天后过期
			} else {
				info.State = false
			}
			p.httpInfoCenter.Put(cookie_guid.Value, info)
		}
	}
}
```

## 特点

```
1. 目的: 将rustful接口转换成grpc访问
2. 客户端不用关心后端服务有哪些，只需知道网关(代理)地址。由网关根据rustful路径自动路由到后端服务并返回对应数据。
3. 同时支持protobuf和json两种协议格式
4. 不支持服务端主动下发消息给客户端
5. 支持路由转发给不同的后端服务
6. grpc转发支持后端服务发现和均衡负载
```

