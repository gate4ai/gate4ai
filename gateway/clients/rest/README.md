# Gateway - REST Client (Placeholder)

This directory is intended for the future gateway client implementation for interacting with external REST API servers registered in the Portal.

**Current Status:** Not Implemented.

**Tasks for Implementation:**

1.  **Interaction Protocol:** Define how the gateway will interact with REST APIs (e.g., simple proxying, translating MCP/A2A to REST).
2.  **Request Handling:** Receive incoming requests (possibly via a dedicated MCP tool or A2A skill), transform them if necessary, and send them to the target REST API.
3.  **Response Handling:** Receive responses from the REST API, transform them if necessary, and return them to the gateway client.
4.  **Authentication:** Support various authentication methods for target REST APIs (API Key, Bearer Token, OAuth, etc.), configured in the Portal.
5.  **Header Injection:** **Important!** When sending requests to the target REST API, it will be necessary to inject the merged set of HTTP headers (System > Server > Subscription), similar to how it's done for MCP and A2A clients. The logic for merging and obtaining headers should be reused.
6.  **Error Handling:** Properly handle network errors and errors from the target API.