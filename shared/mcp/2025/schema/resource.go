package schema

import (
	schema2024 "github.com/gate4ai/mcp/shared/mcp/2024/schema"
)

// Annotations contain optional metadata about objects used by the client.
type Annotations = schema2024.Annotations

// TextResourceContents contains text-based resource content.
// NOTE: Aliased to schema2024.ResourceContent for minimal changes.
// JSON schema 2025 defines this separately.
type TextResourceContents = schema2024.ResourceContent

// BlobResourceContents contains binary resource content.
// NOTE: Aliased to schema2024.ResourceContent for minimal changes.
// JSON schema 2025 defines this separately.
type BlobResourceContents = schema2024.ResourceContent

// ResourceContent represents the actual content of a resource (text or blob).
// NOTE: Aliased to schema2024.ResourceContent for minimal changes.
type ResourceContent = schema2024.ResourceContent

// Content represents various types of message content.
type Content struct {
	// The type discriminator ('text', 'image', 'audio', 'resource').
	Type string `json:"type"`
	// Optional annotations for the client.
	Annotations *Annotations `json:"annotations,omitempty"`
	// Text content (only for type: "text").
	Text *string `json:"text,omitempty"`
	// Base64-encoded data (only for type: "image", "audio").
	Data *string `json:"data,omitempty"`
	// MIME type of the data (only for type: "image", "audio").
	MimeType *string `json:"mimeType,omitempty"`
	// Embedded resource content (only for type: "resource").
	// Can be TextResourceContents or BlobResourceContents.
	Resource *ResourceContent `json:"resource,omitempty"`
}

// NewTextContent creates a new text content slice.
func NewTextContent(text string) []Content {
	t := "text"
	return []Content{
		{
			Type: t,
			Text: &text,
		},
	}
}

// NewImageContent creates a new image content slice.
func NewImageContent(data string, mimeType string) []Content {
	t := "image"
	return []Content{
		{
			Type:     t,
			Data:     &data,
			MimeType: &mimeType,
		},
	}
}

// NewAudioContent creates a new audio content slice.
func NewAudioContent(data string, mimeType string) []Content {
	t := "audio"
	return []Content{
		{
			Type:     t,
			Data:     &data,
			MimeType: &mimeType,
		},
	}
}

// EmbeddedResource represents the contents of a resource embedded into a prompt or tool call result.
type EmbeddedResource struct {
	Type        string          `json:"type"`                  // const: "resource"
	Resource    ResourceContent `json:"resource"`              // Resource contents (Text or Blob)
	Annotations *Annotations    `json:"annotations,omitempty"` // Optional annotations
}

// ListResourcesRequest requests a list of available resources.
// Sent from the client to request a list of resources the server has.
type ListResourcesRequest struct {
	Method string                     `json:"method"` // const: "resources/list"
	Params ListResourcesRequestParams `json:"params,omitempty"`
}

// ListResourcesRequestParams contains parameters for resource listing requests.
type ListResourcesRequestParams struct {
	PaginatedRequestParams // Embeds pagination cursor
}

// ListResourcesResult is the response to a resources list request.
type ListResourcesResult struct {
	PaginatedResult                        // Embeds next cursor
	Meta            map[string]interface{} `json:"_meta,omitempty"` // Reserved for metadata
	Resources       []Resource             `json:"resources"`       // Available resources
}

// ReadResourceRequest requests the content of a resource.
// Sent from the client to the server, to read a specific resource URI.
type ReadResourceRequest struct {
	Method string                    `json:"method"` // const: "resources/read"
	Params ReadResourceRequestParams `json:"params"`
}

// ReadResourceRequestParams contains parameters for resource reading.
type ReadResourceRequestParams struct {
	// The URI of the resource to read. The URI can use any protocol;
	// it is up to the server how to interpret it.
	URI string `json:"uri"` // @format uri
}

// ReadResourceResult contains the content of a requested resource.
// The server's response to a resources/read request from the client.
type ReadResourceResult struct {
	Meta     map[string]interface{} `json:"_meta,omitempty"` // Reserved for metadata
	Contents []ResourceContent      `json:"contents"`        // Resource contents (Text or Blob)
}

// Resource describes a known resource that the server is capable of reading.
type Resource struct {
	Annotations *Annotations `json:"annotations,omitempty"` // Optional annotations
	URI         string       `json:"uri"`                   // The URI of this resource. @format uri
	Name        string       `json:"name"`                  // A human-readable name for this resource
	Description string       `json:"description,omitempty"` // A description of what this resource represents
	MimeType    string       `json:"mimeType,omitempty"`    // The MIME type of this resource, if known
	// Size field removed in 2025 schema
}

// ResourceListChangedNotification informs that available resources have changed.
// An optional notification from the server to the client.
type ResourceListChangedNotification struct {
	Method string                 `json:"method"` // const: "notifications/resources/list_changed"
	Params map[string]interface{} `json:"params,omitempty"`
}

// ResourceReference identifies a resource or resource template definition.
type ResourceReference = schema2024.ResourceReference

// ResourceUpdatedNotification informs that a specific resource has changed.
// Sent from server to client if client subscribed.
type ResourceUpdatedNotification struct {
	Method string                            `json:"method"` // const: "notifications/resources/updated"
	Params ResourceUpdatedNotificationParams `json:"params"`
}

// ResourceUpdatedNotificationParams contains parameters for resource update notification.
type ResourceUpdatedNotificationParams struct {
	// The URI of the resource that has been updated. This might be a sub-resource
	// of the one that the client actually subscribed to.
	URI string `json:"uri"` // @format uri
}

func (runp *ResourceUpdatedNotificationParams) AsMap() map[string]interface{} {
	return map[string]interface{}{
		"uri": runp.URI,
	}
}

// SubscribeRequest requests notifications for a specific resource.
// Sent from the client to request resources/updated notifications from the server.
type SubscribeRequest struct {
	Method string                 `json:"method"` // const: "resources/subscribe"
	Params SubscribeRequestParams `json:"params"`
}

// SubscribeRequestParams contains parameters for subscription requests.
type SubscribeRequestParams struct {
	// The URI of the resource to subscribe to.
	URI string `json:"uri"` // @format uri
}

// UnsubscribeRequest cancels notifications for a specific resource.
// Sent from the client to request cancellation of resources/updated notifications.
type UnsubscribeRequest struct {
	Method string                   `json:"method"` // const: "resources/unsubscribe"
	Params UnsubscribeRequestParams `json:"params"`
}

// UnsubscribeRequestParams contains parameters for unsubscription requests.
type UnsubscribeRequestParams struct {
	// The URI of the resource to unsubscribe from.
	URI string `json:"uri"` // @format uri
}
