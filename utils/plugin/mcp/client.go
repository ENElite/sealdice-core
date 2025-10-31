package mcp

import (
	"context"
	"sync"

	"sealdice-core/logger"

	"github.com/dop251/goja"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// Logger 日志接口，与helper.go中的Helper方法签名一致
type Logger interface {
	Debug(a ...interface{})
	Debugf(format string, a ...interface{})
	Info(a ...interface{})
	Infof(format string, a ...interface{})
	Warn(a ...interface{})
	Warnf(format string, a ...interface{})
	Error(a ...interface{})
	Errorf(format string, a ...interface{})
}

// defaultLogger 默认日志实现（无操作）
type defaultLogger struct{}

func (l *defaultLogger) Debug(_ ...interface{})            {}
func (l *defaultLogger) Debugf(_ string, _ ...interface{}) {}
func (l *defaultLogger) Info(_ ...interface{})             {}
func (l *defaultLogger) Infof(_ string, _ ...interface{})  {}
func (l *defaultLogger) Warn(_ ...interface{})             {}
func (l *defaultLogger) Warnf(_ string, _ ...interface{})  {}
func (l *defaultLogger) Error(args ...interface{}) {
	// 默认情况下只输出错误日志到标准错误
	logger.M().Errorf("[MCP ERROR] %v", args...)
}
func (l *defaultLogger) Errorf(format string, args ...interface{}) {
	// 默认情况下只输出错误日志到标准错误
	logger.M().Errorf("[MCP ERROR] "+format, args...)
}

// WebSocketLogger 全局日志实例，用户可以通过SetLogger函数替换
var MCPLogger Logger = &defaultLogger{}

// SetLogger 设置全局日志实例
func SetLogger(logger Logger) {
	MCPLogger = logger
}

type (
	ClientInstance struct {
		rt        *goja.Runtime
		jsObject  *goja.Object
		url       string
		http      *transport.StreamableHTTP
		mcpClient *client.Client
		ctx       context.Context
	}

	ClientManager struct {
		clients []*ClientInstance
		mutex   sync.Mutex
	}
)

// GlobalConnManager 是一个全局的WebSocket管理器 用来最后优雅销毁的
var GlobalClientManager = &ClientManager{}

// Register 注册连接
func (m *ClientManager) Register(client *ClientInstance) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.clients = append(m.clients, client)
}

// Unregister 移除已关闭的连接
func (m *ClientManager) Unregister(client *ClientInstance) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	for i, c := range m.clients {
		if c == client {
			m.clients = append(m.clients[:i], m.clients[i+1:]...)
			break
		}
	}
}

// closeWithoutUnregister 关闭连接但不从管理器注销（用于CloseAll）
func (client *ClientInstance) closeWithoutUnregister(args ...interface{}) {
	client.mcpClient.Close()
}

// CloseAll 关闭所有连接
func (m *ClientManager) CloseAll() {
	// 获取连接副本并清空原列表
	m.mutex.Lock()
	clientsCopy := make([]*ClientInstance, len(m.clients))
	copy(clientsCopy, m.clients)
	m.clients = m.clients[:0]
	m.mutex.Unlock()

	// 在锁外关闭连接，避免死锁
	for _, client := range clientsCopy {
		if client != nil {
			client.closeWithoutUnregister() // 使用新方法避免重复注销
		}
	}
}

// mcp.Client() 实例绑定方法
func (ci *ClientInstance) bindClientMethods() {
	rt := ci.rt
	obj := rt.NewObject()

	_ = obj.DefineAccessorProperty("url", rt.ToValue(func() string {
		return ci.url
	}), goja.Undefined(), goja.FLAG_FALSE, goja.FLAG_TRUE)

	_ = obj.Set("connect", func(url string) {
		httpTransport, err := transport.NewStreamableHTTP(url)
		if err != nil {
			panic(rt.ToValue("Failed to create HTTP transport: " + err.Error()))
		}

		ci.http = httpTransport

		mcpClient := client.NewClient(
			httpTransport,
		)
		err = mcpClient.Start(ci.ctx)
		if err != nil {
			panic(rt.ToValue("Failed to start MCP client: " + err.Error()))
		}

		ci.url = url
		ci.mcpClient = mcpClient

		initResult, err := mcpClient.Initialize(ci.ctx, mcp.InitializeRequest{
			Params: mcp.InitializeParams{
				ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
				ClientInfo: mcp.Implementation{
					Name:    "sealdice-core",
					Version: "0.1.0",
				},
				Capabilities: mcp.ClientCapabilities{},
			},
		})
		if err != nil {
			panic(rt.ToValue("Failed to initialize MCP client: " + err.Error()))
		}

		MCPLogger.Infof("Connected to MCP Server: %s v%s", initResult.ServerInfo.Name, initResult.ServerInfo.Version)
		// MCPLogger.Infof("Server capabilities: %+v", initResult.Capabilities)
	})

	_ = obj.Set("close", func() {
		if ci.mcpClient != nil {
			ci.mcpClient.Close()
		}
		GlobalClientManager.Unregister(ci)
	})

	_ = obj.Set("listTools", func() []mcp.Tool {
		res, err := ci.mcpClient.ListTools(ci.ctx, mcp.ListToolsRequest{})
		if err != nil {
			panic(rt.ToValue("Failed to listTools: " + err.Error()))
		}
		return res.Tools
	})

	_ = obj.Set("callTool", func(tool string, input map[string]any) []string {
		// var argument map[string]any

		// err := json.Unmarshal([]byte(input), &argument)

		// if err != nil {
		// 	panic(rt.ToValue("Error tool argument: " + err.Error()))
		// }

		result, err := ci.mcpClient.CallTool(ci.ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Name:      tool,
				Arguments: input,
			},
		})

		if err != nil {
			panic(rt.ToValue("Error calling tool: " + err.Error()))
		}

		res := make([]string, 0)

		for _, content := range result.Content {
			if text, ok := content.(mcp.TextContent); ok {
				res = append(res, text.Text)
			}
		}
		return res
	})

	ci.jsObject = obj
}

// 绑定 mcp.Client(url: string) -> Clientt 构造函数和静态方法
func NewClient(rt *goja.Runtime) goja.Value {
	// 使用DefineConstructor创建构造函数
	constructorFunc := func(call goja.ConstructorCall) *goja.Object {
		args := call.Arguments

		client := &ClientInstance{
			rt:  rt,
			ctx: context.Background(),
		}
		if len(args) >= 1 {
			client.url = args[0].String()
		}
		GlobalClientManager.Register(client)

		client.bindClientMethods()
		return client.jsObject
	}

	clientConstructor := rt.ToValue(constructorFunc)
	// constructorObj := clientConstructor.ToObject(ci.rt)

	return clientConstructor
}

// Enable 为给定的goja运行时启用WebSocket模块
// 这是一个便利函数，用于快速设置WebSocket模块
func Enable(rt *goja.Runtime) {
	mcp := rt.NewObject()
	_ = mcp.Set("Client", NewClient(rt))
	// _ = mcp.Set("Server", nil) // 服务器端功能未实现

	_ = rt.Set("mcp", mcp)

}
