package exampleCapability

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/gate4ai/mcp/server"
	"github.com/gate4ai/mcp/server/mcp/capability"
	"go.uber.org/zap"

	// Removed capability import here, options will ensure creation
	"github.com/gate4ai/mcp/shared" // Keep for input schema if needed
	schema "github.com/gate4ai/mcp/shared/mcp/2025/schema"
)

// Define Tool handlers directly in this package
var EchoTool = schema.Tool{
	Name:        "echo",
	Description: "echo a message",
	InputSchema: &schema.JSONSchemaProperty{
		Type: "object",
		Properties: map[string]schema.JSONSchemaProperty{
			"message": {
				Type:        "string",
				Description: "The message to echo back",
			},
		},
		Required: []string{"message"},
	},
}

func EchoToolHandler(_ *shared.Message, arguments schema.Arguments) (*schema.Meta, []schema.Content, error) {
	message, ok := arguments["message"].(string)
	if !ok {
		return nil, nil, fmt.Errorf("invalid 'message' argument type: expected string")
	}
	text := "Echo: " + message
	return nil, []schema.Content{{
		Type: "text",
		Text: &text,
	}}, nil
}

var AddTool = schema.Tool{
	Name:        "add",
	Description: "add two numbers",
	InputSchema: &schema.JSONSchemaProperty{ // Use 2025 schema here too
		Type: "object",
		Properties: map[string]schema.JSONSchemaProperty{
			"a": {
				Type:        "number",
				Description: "First number to add",
			},
			"b": {
				Type:        "number",
				Description: "Second number to add",
			},
		},
		Required: []string{"a", "b"},
	},
}

func AddToolHandler(_ *shared.Message, arguments schema.Arguments) (*schema.Meta, []schema.Content, error) {
	// Safely convert arguments to float64
	aFloat, okA := arguments["a"].(float64)
	bFloat, okB := arguments["b"].(float64)
	if !okA || !okB {
		return nil, nil, fmt.Errorf("invalid number arguments: expected float64")
	}
	result := strconv.Itoa(int(aFloat + bFloat))
	return nil, []schema.Content{{
		Type: "text",
		Text: &result,
	}}, nil
}

var LongRunningTool = schema.Tool{
	Name:        "longRunningOperation",
	Description: "long running operation",
	InputSchema: &schema.JSONSchemaProperty{
		Type: "object",
		Properties: map[string]schema.JSONSchemaProperty{
			"duration": {Type: "number"},
			"steps":    {Type: "number"},
		},
		Required: []string{"duration"},
	},
}

func LongRunningHandler(_ *shared.Message, arguments schema.Arguments) (*schema.Meta, []schema.Content, error) {
	durationFloat, ok := arguments["duration"].(float64)
	if !ok {
		return nil, nil, fmt.Errorf("invalid 'duration' argument type: expected number")
	}
	// steps, _ := arguments["steps"].(float64) // Ignoring steps for now

	time.Sleep(time.Duration(durationFloat) * time.Second)

	result := "completed"
	return nil, []schema.Content{{
		Type: "text",
		Text: &result,
	}}, nil
}

var SampleLLMTool = schema.Tool{
	Name:        "sampleLLM",
	Description: "The prompt to send to the LLM",
	InputSchema: &schema.JSONSchemaProperty{
		Type: "object",
		Properties: map[string]schema.JSONSchemaProperty{
			"prompt":    {Type: "string"},
			"maxTokens": {Type: "number"},
		},
		Required: []string{"prompt", "maxTokens"},
	},
}

func SampleLLMHandler(msg *shared.Message, arguments schema.Arguments) (*schema.Meta, []schema.Content, error) {
	prompt, okP := arguments["prompt"].(string)
	maxTokensFloat, okM := arguments["maxTokens"].(float64)
	if !okP || !okM {
		return nil, nil, fmt.Errorf("invalid arguments for sampleLLM")
	}

	text := "Resource sampleLLM context: " + prompt

	// Get the session from the message
	response := <-msg.Session.SendRequestSync(
		"sampling/createMessage",
		schema.CreateMessageRequestParams{
			Messages: []schema.SamplingMessage{{
				Role: "user",
				Content: schema.Content{
					Type: "text",
					Text: &text,
				},
			}},
			SystemPrompt:   "You are a helpful test server.",
			MaxTokens:      int(maxTokensFloat),
			Temperature:    shared.PointerTo(0.7), // Use helper
			IncludeContext: "thisServer",
		},
	)

	if response.Error != nil {
		return nil, nil, response.Error
	}

	var createMessageResult schema.CreateMessageResult
	if err := json.Unmarshal(*response.Result, &createMessageResult); err != nil {
		return nil, nil, err
	}

	resultStr := "LLM sampling result: " + *createMessageResult.Content.Text
	return nil, []schema.Content{{
		Type: "text",
		Text: &resultStr,
	}}, nil
}

var TinyImageTool = schema.Tool{
	Name:        "getTinyImage",
	Description: "Returns the MCP_TINY_IMAGE",
	InputSchema: &schema.JSONSchemaProperty{
		Type:       "object",
		Properties: map[string]schema.JSONSchemaProperty{},
		Required:   []string{},
	},
}

func TinyImageHandler(_ *shared.Message, arguments schema.Arguments) (*schema.Meta, []schema.Content, error) {
	text1 := "This is a tiny image:"
	mimeType := "image/png"
	data := "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"
	text2 := "The image above is the MCP tiny image."
	return nil, []schema.Content{{
		Type: "text",
		Text: &text1,
	}, {
		Type:     "image",
		Data:     &data,
		MimeType: &mimeType,
	}, {
		Type: "text",
		Text: &text2,
	}}, nil
}

var PrintEnvTool = schema.Tool{
	Name:        "printEnv",
	Description: "Returns the printEnv",
	InputSchema: &schema.JSONSchemaProperty{
		Type:       "object",
		Properties: map[string]schema.JSONSchemaProperty{},
		Required:   []string{},
	},
}

func PrintEnvHandler(_ *shared.Message, arguments schema.Arguments) (*schema.Meta, []schema.Content, error) {
	text := "{\"Fake\": \"envs\"}"
	return nil, []schema.Content{{
		Type: "text",
		Text: &text,
	}}, nil
}

// --- Prompt Definitions ---
var SimplePrompt = schema.Prompt{
	Name:        "simple_prompt",
	Description: "A simple prompt without arguments",
}

func SimplePromptHandler(msg *shared.Message) (*schema.Meta, []schema.PromptMessage, error) {
	responseText := "This is a simple prompt without arguments."
	return nil, []schema.PromptMessage{{
		Role: "user",
		Content: schema.Content{
			Type: "text",
			Text: &responseText,
		},
	}}, nil
}

var ComplexPromptTemplate = schema.Prompt{
	Name:        "complex_prompt",
	Description: "Advanced prompt demonstrating argument handling",
	Arguments: []schema.PromptArgument{
		{Name: "temperature", Description: "Sampling temperature", Required: true},
		{Name: "style", Description: "Generation style", Required: false},
	},
}

func ComplexPromptHandler(msg *shared.Message) (*schema.Meta, []schema.PromptMessage, error) {
	// Parse parameters from the message
	var params schema.GetPromptRequestParams
	if msg.Params != nil {
		if err := json.Unmarshal(*msg.Params, &params); err != nil {
			return nil, nil, fmt.Errorf("failed to parse parameters: %v", err)
		}
	}

	// Get the temperature parameter (required)
	tempStr, ok := params.Arguments["temperature"]
	if !ok {
		return nil, nil, fmt.Errorf("missing required parameter: temperature")
	}

	// Get the style parameter (optional)
	style, hasStyle := params.Arguments["style"]
	if !hasStyle {
		style = "standard" // Default style
	}

	// Construct the multi-turn conversation with image
	userText := fmt.Sprintf("This is a complex prompt using temperature: %s and style: %s", tempStr, style)
	assistantText := "I'll demonstrate a multi-turn conversation with image response."
	userFollowupText := "Thanks for the detailed response with image!"

	// Create and encode a sample image
	mimeType := "image/png"
	data := "iVBORw0KGgoAAAANSUhEUgAAABQAAAAUCAYAAACNiR0NAAAKsGlDQ1BJQ0MgUHJvZmlsZQAASImVlwdUU+kSgOfe9JDQEiIgJfQmSCeAlBBaAAXpYCMkAUKJMRBU7MriClZURLCs6KqIgo0idizYFsWC3QVZBNR1sWDDlXeBQ9jdd9575805c+a7c+efmf+e/z9nLgCdKZDJMlF1gCxpjjwyyI8dn5DIJvUABRiY0kBdIMyWcSMiwgCTUft3+dgGyJC9YzuU69/f/1fREImzhQBIBMbJomxhFsbHMe0TyuQ5ALg9mN9kbo5siK9gzJRjDWL8ZIhTR7hviJOHGY8fjomO5GGsDUCmCQTyVACaKeZn5wpTsTw0f4ztpSKJFGPsGbyzsmaLMMbqgiUWI8N4KD8n+S95Uv+WM1mZUyBIVfLIXoaF7C/JlmUK5v+fn+N/S1amYrSGOaa0NHlwJGaxvpAHGbNDlSxNnhI+yhLRcPwwpymCY0ZZmM1LHGWRwD9UuTZzStgop0gC+co8OfzoURZnB0SNsnx2pLJWipzHHWWBfKyuIiNG6U8T85X589Ki40Y5VxI7ZZSzM6JCx2J4Sr9cEansXywN8hurG6jce1b2X/Yr4SvX5qRFByv3LhjrXyzljuXMjlf2JhL7B4zFxCjjZTl+ylqyzAhlvDgzSOnPzo1Srs3BDuTY2gjlN0wXhESMMoRBELAhBjIhB+QggECQgBTEOeJ5Q2cUeLNl8+WS1LQcNhe7ZWI2Xyq0m8B2tHd0Bhi6syNH4j1r+C4irGtjvhWVAF4nBgcHT475Qm4BHEkCoNaO+SxnAKh3A1w5JVTIc0d8Q9cJCEAFNWCCDhiACViCLTiCK3iCLwRACIRDNCTATBBCGmRhnc+FhbAMCqAI1sNmKIOdsBv2wyE4CvVwCs7DZbgOt+AePIZ26IJX0AcfYQBBEBJCRxiIDmKImCE2iCPCQbyRACQMiUQSkCQkFZEiCmQhsgIpQoqRMmQXUokcQU4g55GrSCvyEOlAepF3yFcUh9JQJqqPmqMTUQ7KRUPRaHQGmorOQfPQfHQtWopWoAfROvQ8eh29h7ajr9B+HOBUcCycEc4Wx8HxcOG4RFwKTo5bjCvEleAqcNW4Rlwz7g6uHfca9wVPxDPwbLwt3hMfjI/BC/Fz8Ivxq/Fl+P34OvxF/B18B74P/51AJ+gRbAgeBD4hnpBKmEsoIJQQ9hJqCZcI9whdhI9EIpFFtCC6EYOJCcR04gLiauJ2Yg3xHLGV2EnsJ5FIOiQbkhcpnCQg5ZAKSFtJB0lnSbdJXaTPZBWyIdmRHEhOJEvJy8kl5APkM+Tb5G7yAEWdYkbxoIRTRJT5lHWUPZRGyk1KF2WAqkG1oHpRo6np1GXUUmo19RL1CfW9ioqKsYq7ylQVicpSlVKVwypXVDpUvtA0adY0Hm06TUFbS9tHO0d7SHtPp9PN6b70RHoOfS29kn6B/oz+WZWhaqfKVxWpLlEtV61Tva36Ro2iZqbGVZuplqdWonZM7abaa3WKurk6T12gvli9XP2E+n31fg2GhoNGuEaWxmqNAxpXNXo0SZrmmgGaIs18zd2aFzQ7GTiGCYPHEDJWMPYwLjG6mESmBZPPTGcWMQ8xW5h9WppazlqxWvO0yrVOa7WzcCxzFp+VyVrHOspqY30dpz+OO048btW46nG3x33SHq/tqy3WLtSu0b6n/VWHrROgk6GzQade56kuXtdad6ruXN0dupd0X49njvccLxxfOP7o+Ed6qJ61XqTeAr3dejf0+vUN9IP0Zfpb9S/ovzZgGfgapBtsMjhj0GvIMPQ2lBhuMjxr+JKtxeayM9ml7IvsPiM9o2AjhdEuoxajAWML4xjj5cY1xk9NqCYckxSTTSZNJn2mhqaTTReaVpk+MqOYcczSzLaYNZt9MrcwjzNfaV5v3mOhbcG3yLOosnhiSbf0sZxjWWF514poxbHKsNpudcsatXaxTrMut75pg9q42khsttu0TiBMcJ8gnVAx4b4tzZZrm2tbZdthx7ILs1tuV2/3ZqLpxMSJGyY2T/xu72Kfab/H/rGDpkOIw3KHRod3jtaOQsdyx7tOdKdApyVODU5vnW2cxc47nB+4MFwmu6x0aXL509XNVe5a7drrZuqW5LbN7T6HyYngrOZccSe4+7kvcT/l/sXD1SPH46jHH562nhmeBzx7JllMEk/aM6nTy9hL4LXLq92b7Z3k/ZN3u4+Rj8Cnwue5r4mvyHevbzfXipvOPch942fvJ/er9fvE8+At4p3zx/kH+Rf6twRoBsQElAU8CzQOTA2sCuwLcglaEHQumBAcGrwh+D5fny/kV/L7QtxCFoVcDKWFRoWWhT4Psw6ThzVORieHTN44+ckUsynSKfXhEM4P3xj+NMIiYk7EyanEqRFTy6e+iHSIXBjZHMWImhV1IOpjtF/0uujHMZYxipimWLXY6bGVsZ/i/OOK49rjJ8Yvir+eoJsgSWhIJCXGJu5N7J8WMG3ztK7pLtMLprfNsJgxb8bVmbozM2eenqU2SzDrWBIhKS7pQNI3QbigQtCfzE/eltwn5Am3CF+JfEWbRL1iL3GxuDvFK6U4pSfVK3Vjam+aT1pJ2msJT1ImeZsenL4z/VNGeMa+jMHMuMyaLHJWUtYJqaY0Q3pxtsHsebNbZTayAln7HI85m+f0yUPle7OR7BnZDTlMbDi6obBU/KDoyPXOLc/9PDd27rF5GvOk827Mt56/an53XmDezwvwC4QLmhYaLVy2sGMRd9Guxcji5MVNS0yW5C/pWhq0dP8y6rKMZb8st19evPzDirgVjfn6+UvzO38I+qGqQLVAXnB/pefKnT/if5T82LLKadXWVd8LRYXXiuyLSoq+rRauvrbGYU3pmsG1KWtb1rmu27GeuF66vm2Dz4b9xRrFecWdGydvrNvE3lS46cPmWZuvljiX7NxC3aLY0l4aVtqw1XTr+q3fytLK7pX7ldds09u2atun7aLtt3f47qjeqb+zaOfXnyQ/PdgVtKuuwryiZDdxd+7uF3ti9zT/zPm5cq/u3qK9f+6T7mvfH7n/YqVbZeUBvQPrqtAqRVXvwekHbx3yP9RQbVu9q4ZVU3QYDisOvzySdKTtaOjRpmOcY9XHzY5vq2XUFtYhdfPr+urT6tsbEhpaT4ScaGr0bKw9aXdy3ymjU+WntU6vO0M9k39m8Gze2f5zsnOvz6ee72ya1fT4QvyFuxenXmy5FHrpyuXAyxeauc1nr3hdOXXV4+qJa5xr9dddr9fdcLlR+4vLL7Utri11N91uNtzyv9XYOqn1zG2f2+fv+N+5fJd/9/q9Kfda22LaHtyffr/9gehBz8PMh28f5T4aeLz0CeFJ4VP1pyXP9J5V/Gr1a027a/vpDv+OG8+jnj/uFHa++i37t29d+S/oL0q6Dbsrexx7TvUG9t56Oe1l1yvZq4HXBb9r/L7tjeWb43/4/nGjL76v66387eC71e913u/74PyhqT+i/9nHrI8Dnwo/63ze/4Xzpflr3NfugbnfSN9K/7T6s/F76Pcng1mDgzKBXDA8CuAwRVNSAN7tA6AnADCwGYI6bWSmHhZk5D9gmOA/8cjcPSyuANWYGRqNeOcADmNqvhRAzRdgaCyK9gXUyUmpo/Pv8Kw+JAbYv8K0HECi2x6tebQU/iEjc/xf+v6nBWXWv9l/AV0EC6JTIblRAAAAeGVYSWZNTQAqAAAACAAFARIAAwAAAAEAAQAAARoABQAAAAEAAABKARsABQAAAAEAAABSASgAAwAAAAEAAgAAh2kABAAAAAEAAABaAAAAAAAAAJAAAAABAAAAkAAAAAEAAqACAAQAAAABAAAAFKADAAQAAAABAAAAFAAAAAAXNii1AAAACXBIWXMAABYlAAAWJQFJUiTwAAAB82lUWHRYTUw6Y29tLmFkb2JlLnhtcAAAAAAAPHg6eG1wbWV0YSB4bWxuczp4PSJhZG9iZTpuczptZXRhLyIgeDp4bXB0az0iWE1QIENvcmUgNi4wLjAiPgogICA8cmRmOlJERiB4bWxuczpyZGY9Imh0dHA6Ly93d3cudzMub3JnLzE5OTkvMDIvMjItcmRmLXN5bnRheC1ucyMiPgogICAgICA8cmRmOkRlc2NyaXB0aW9uIHJkZjphYm91dD0iIgogICAgICAgICAgICB4bWxuczp0aWZmPSJodHRwOi8vbnMuYWRvYmUuY29tL3RpZmYvMS4wLyI+CiAgICAgICAgIDx0aWZmOllSZXNvbHV0aW9uPjE0NDwvdGlmZjpZUmVzb2x1dGlvbj4KICAgICAgICAgPHRpZmY6T3JpZW50YXRpb24+MTwvdGlmZjpPcmllbnRhdGlvbj4KICAgICAgICAgPHRpZmY6WFJlc29sdXRpb24+MTQ0PC90aWZmOlhSZXNvbHV0aW9uPgogICAgICAgICA8dGlmZjpSZXNvbHV0aW9uVW5pdD4yPC90aWZmOlJlc29sdXRpb25Vbml0PgogICAgICA8L3JkZjpEZXNjcmlwdGlvbj4KICAgPC9yZGY6UkRGPgo8L3g6eG1wbWV0YT4KReh49gAAAjRJREFUOBGFlD2vMUEUx2clvoNCcW8hCqFAo1dKhEQpvsF9KrWEBh/ALbQ0KkInBI3SWyGPCCJEQliXgsTLefaca/bBWjvJzs6cOf/fnDkzOQJIjWm06/XKBEGgD8c6nU5VIWgBtQDPZPWtJE8O63a7LBgMMo/Hw0ql0jPjcY4RvmqXy4XMjUYDUwLtdhtmsxnYbDbI5/O0djqdFFKmsEiGZ9jP9gem0yn0ej2Yz+fg9XpfycimAD7DttstQTDKfr8Po9GIIg6Hw1Cr1RTgB+A72GAwgMPhQLBMJgNSXsFqtUI2myUo18pA6QJogefsPrLBX4QdCVatViklw+EQRFGEj88P2O12pEUGATmsXq+TaLPZ0AXgMRF2vMEqlQoJTSYTpNNpApvNZliv1/+BHDaZTAi2Wq1A3Ig0xmMej7+RcZjdbodUKkWAaDQK+GHjHPnImB88JrZIJAKFQgH2+z2BOczhcMiwRCIBgUAA+NN5BP6mj2DYff35gk6nA61WCzBn2JxO5wPM7/fLz4vD0E+OECfn8xl/0Gw2KbLxeAyLxQIsFgt8p75pDSO7h/HbpUWpewCike9WLpfB7XaDy+WCYrFI/slk8i0MnRRAUt46hPMI4vE4+Hw+ec7t9/44VgWigEeby+UgFArJWjUYOqhWG6x50rpcSfR6PVUfNOgEVRlTX0HhrZBKz4MZjUYWi8VoA+lc9H/VaRZYjBKrtXR8tlwumcFgeMWRbZpA9ORQWfVm8A/FsrLaxebd5wAAAABJRU5ErkJggg=="

	return nil, []schema.PromptMessage{
		{
			Role: "user",
			Content: schema.Content{
				Type: "text",
				Text: &userText,
			},
		},
		{
			Role: "assistant",
			Content: schema.Content{
				Type: "text",
				Text: &assistantText,
			},
		},
		{
			Role: "assistant",
			Content: schema.Content{
				Type:     "image",
				Data:     &data,
				MimeType: &mimeType,
			},
		},
		{
			Role: "user",
			Content: schema.Content{
				Type: "text",
				Text: &userFollowupText,
			},
		},
	}, nil
}

// --- Resource Definitions ---
func ResourceHandlerOdd(i int) capability.ResourceHandler {
	return func(msg *shared.Message) (schema.Meta, []schema.ResourceContent, error) {
		uri := fmt.Sprintf("test://static/resource/%d", i)
		text := fmt.Sprintf("Resource %d: This is a plaintext resource", i)
		return nil, []schema.ResourceContent{{
			URI:      uri,
			MimeType: "text/plain",
			Text:     &text,
		}}, nil
	}
}

func ResourceHandlerEven(i int) capability.ResourceHandler {
	return func(msg *shared.Message) (schema.Meta, []schema.ResourceContent, error) {
		uri := fmt.Sprintf("test://static/resource/%d", i)
		data := fmt.Sprintf("Resource %d: This is a base64 blob", i)
		encodedData := base64.StdEncoding.EncodeToString([]byte(data))
		return nil, []schema.ResourceContent{{
			URI:      uri,
			MimeType: "application/octet-stream",
			Blob:     &encodedData,
		}}, nil
	}
}

// --- Completion Handler ---
func CompletionHandler(msg *shared.Message, arg schema.CompleteArgument) (*schema.CompletionInfo, error) {
	// Simple example: suggest based on prefix
	suggestions := []string{}
	if arg.Name == "message" && arg.Value == "Hel" {
		suggestions = append(suggestions, "Hello", "Help")
	} else if arg.Name == "a" && arg.Value == "1" {
		suggestions = append(suggestions, "10", "100")
	}
	return &schema.CompletionInfo{Values: suggestions}, nil
}

// --- Subscription Handler ---
func SubscriptionLogger(logger *zap.Logger) capability.SubscriptionHandler {
	return func(session shared.ISession, operation capability.SubscriptionOperation, uri string, count int) {
		opStr := "subscribed"
		if operation == capability.Unsubscribe {
			opStr = "unsubscribed"
		}
		logger.Info("Subscription event",
			zap.String("operation", opStr),
			zap.String("uri", uri),
			zap.String("sessionID", session.GetID()),
			zap.Int("currentCount", count),
		)
	}
}

// BuildOptions creates the ServerOption slice for the example server.
func BuildOptions(logger *zap.Logger) []server.ServerOption {
	options := []server.ServerOption{
		// Add tools
		server.WithMCPTool(EchoTool.Name, EchoTool.Description, EchoTool.InputSchema, EchoTool.Annotations, EchoToolHandler),
		server.WithMCPTool(AddTool.Name, AddTool.Description, AddTool.InputSchema, AddTool.Annotations, AddToolHandler),
		server.WithMCPTool(LongRunningTool.Name, LongRunningTool.Description, LongRunningTool.InputSchema, LongRunningTool.Annotations, LongRunningHandler),
		server.WithMCPTool(SampleLLMTool.Name, SampleLLMTool.Description, SampleLLMTool.InputSchema, SampleLLMTool.Annotations, SampleLLMHandler),
		server.WithMCPTool(TinyImageTool.Name, TinyImageTool.Description, TinyImageTool.InputSchema, TinyImageTool.Annotations, TinyImageHandler),
		server.WithMCPTool(PrintEnvTool.Name, PrintEnvTool.Description, PrintEnvTool.InputSchema, PrintEnvTool.Annotations, PrintEnvHandler),

		// Add prompts
		server.WithMCPPrompt(SimplePrompt.Name, SimplePrompt.Description, SimplePromptHandler),
		server.WithMCPPromptTemplate(ComplexPromptTemplate.Name, ComplexPromptTemplate.Description, ComplexPromptTemplate.Arguments, ComplexPromptHandler),

		// Add resource template
		server.WithMCPResourceTemplate("test://static/resource/{id}", "Static Resource Template", "Template for static resources", "text/plain", nil), // Handler can be nil for template definition

		// Add completion capability
		//server.WithMCPCompletionCapability(CompletionHandler),

		// Add subscription handler
		server.WithMCPSubscriptionHandler(SubscriptionLogger(logger)),
	}

	// Add static resources
	for i := 1; i <= 10; i++ {
		uri := fmt.Sprintf("test://static/resource/%d", i)
		resourceName := fmt.Sprintf("Resource %d", i)
		var mimeType string
		var handler capability.ResourceHandler
		if i%2 == 1 {
			mimeType = "text/plain"
			handler = ResourceHandlerOdd(i)
		} else {
			mimeType = "application/octet-stream"
			handler = ResourceHandlerEven(i)
		}
		options = append(options, server.WithMCPResource(uri, resourceName, "Static resource", mimeType, handler))
	}

	return options
}
