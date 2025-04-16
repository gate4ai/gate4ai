package clients

type ServerProtocol string

const (
	ServerTypeMCP  ServerProtocol = "MCP"
	ServerTypeA2A  ServerProtocol = "A2A"
	ServerTypeREST ServerProtocol = "REST"
)

type ServerInfo struct {
	URL             string         `json:"url"`             // a2a:AgentCard.URL      mcp: just the URL
	Name            string         `json:"name"`            // AgentCard.Name         mcp: InitializeResult.ServerInfo.Name
	Version         string         `json:"version"`         // AgentCard.Version      mcp: InitializeResult.ServerInfo.Version
	Description     string         `json:"description"`     // AgentCard.Description  mcp: empty
	Website         *string        `json:"website"`         // AgentCard.Provider.URL mcp: empty
	Protocol        ServerProtocol `json:"protocol"`        // MCP or A2A or REST
	ProtocolVersion string         `json:"protocolVersion"` // MCP or A2A or REST
}

type IClient interface {
	ServerInfo() ServerInfo
}
