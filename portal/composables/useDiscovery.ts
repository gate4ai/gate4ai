import { ref, reactive, readonly } from "vue";
import { useNuxtApp } from "#app";
import { useSnackbar } from "./useSnackbar";
import type { ServerProtocol } from "@prisma/client"; // Assuming prisma types available

// --- Interface Definitions (Matching AddServerDialog) ---
interface LogDetails {
  type?: string;
  message?: string;
  statusCode?: number;
  responseBodyPreview?: string;
}
interface DiscoveryLogEntry {
  stepId: string;
  timestamp: string;
  protocol: string;
  method: string;
  step: string;
  url?: string;
  status: "attempting" | "success" | "error";
  details?: LogDetails;
}
interface DiscoveredTool {
  name: string;
  description?: string;
  inputSchema?: Record<string, unknown>;
}
interface DiscoveredSkill {
  id: string;
  name: string;
  description?: string | null;
  tags?: string[];
  examples?: string[];
  inputModes?: string[];
  outputModes?: string[];
}
interface DiscoveringResponse {
  url: string;
  name: string;
  version: string;
  description: string;
  website: string | null;
  protocol: ServerProtocol | ""; // Can be empty if none found
  protocolVersion: string;
  mcpTools?: DiscoveredTool[];
  a2aSkills?: DiscoveredSkill[];
  restEndpoints?: Array<Record<string, unknown>>;
  error?: string; // Specific error string from discovery endpoint
}

// --- Composable Logic ---
export function useDiscovery() {
  const { $settings, $auth } = useNuxtApp();
  const { showError } = useSnackbar();

  const isDiscoveringSSE = ref(false);
  const fetchError = ref(""); // Stores errors *during* the SSE request/stream setup
  const discoveryLogMap = reactive<Map<string, DiscoveryLogEntry>>(new Map());
  const eventSource = ref<EventSource | null>(null);
  const discoveredInfo = ref<DiscoveringResponse | null>(null);
  const discoveredProtocol = ref<ServerProtocol | "UNKNOWN" | "ERROR" | null>(
    null
  );

  // --- Methods ---
  function closeEventSource() {
    if (eventSource.value) {
      console.log("Closing EventSource connection.");
      eventSource.value.close();
      eventSource.value = null;
    }
    // Don't reset isDiscoveringSSE here, handleFinalResult does that
  }

  function resetDiscoveryState() {
    closeEventSource();
    isDiscoveringSSE.value = false;
    fetchError.value = "";
    discoveryLogMap.clear();
    discoveredInfo.value = null;
    discoveredProtocol.value = null;
  }

  function handleFinalResult(
    data: DiscoveringResponse
  ): DiscoveringResponse | null {
    console.log("Received final_result:", data);
    isDiscoveringSSE.value = false; // Mark discovery as finished
    closeEventSource(); // Ensure connection is closed

    const result = data as DiscoveringResponse;
    discoveredInfo.value = result; // Store the full result

    if (result.error) {
      discoveredProtocol.value = "ERROR";
      // Don't set fetchError here anymore, just show snackbar
      showError(`Discovery failed: ${result.error}`);
    } else if (result.protocol) {
      discoveredProtocol.value = result.protocol;
      // Update state based on protocol (caller can use discoveredInfo)
    } else {
      discoveredProtocol.value = "UNKNOWN";
      // Show snackbar error, but don't prevent moving to step 2 or hide logs
      showError("Could not determine server type. Check logs for details.");
    }
    return result; // Return the processed result
  }

  async function startDiscoverySSE(
    targetUrl: string,
    headers: Record<string, string>
  ): Promise<DiscoveringResponse | null> {
    // Ensure previous state is cleared before starting
    resetDiscoveryState();
    isDiscoveringSSE.value = true;
    fetchError.value = ""; // Clear previous fetch errors

    // Wrap the core logic in a promise to await the final result
    return new Promise(async (resolve, reject) => {
      try {
        const gatewayAddress = $settings.get("general_gateway_address") as
          | string
          | undefined;
        const discoveringHandlerPath = $settings.get(
          "path_for_discovering_handler"
        ) as string | undefined;

        if (!discoveringHandlerPath) {
          throw new Error("Gateway discovery path not configured.");
        }

        const effectiveGatewayAddress =
          gatewayAddress || window.location.origin;
        const discoveryUrlPath = discoveringHandlerPath.startsWith("/")
          ? discoveringHandlerPath
          : `/${discoveringHandlerPath}`;
        const fullDiscoveryUrl = `${effectiveGatewayAddress}${discoveryUrlPath}`;

        console.log(`Initiating SSE discovery to: ${fullDiscoveryUrl}`);

        const headersToSend = Object.entries(headers)
          .filter(([k, v]) => k.trim() !== "" && v.trim() !== "")
          .reduce((obj, [k, v]) => {
            obj[k.trim()] = v;
            return obj;
          }, {} as Record<string, string>);

        const requestPayload = {
          targetUrl: targetUrl,
          headers: headersToSend,
        };

        // Use fetch API directly for SSE control
        const response = await fetch(fullDiscoveryUrl, {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            Accept: "text/event-stream",
            Authorization: "Bearer " + $auth.getToken(),
          },
          body: JSON.stringify(requestPayload),
          // Add signal for cancellation if needed later
        });

        if (!response.ok) {
          let errorBody = `HTTP error ${response.status}`;
          try {
            const errorJson = await response.json();
            errorBody = errorJson.message || errorJson.error || errorBody;
          } catch {
            /* ignore json parsing error */
          }
          throw new Error(errorBody);
        }

        if (!response.body) {
          throw new Error("Response body is null, cannot read SSE stream.");
        }

        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";
        let currentEventType = "message"; // Default SSE event type

        while (true) {
          const { done, value } = await reader.read();
          if (done) {
            console.log("SSE stream finished by server.");
            // If discovery is still marked as active, it means final_result wasn't received
            if (isDiscoveringSSE.value) {
              fetchError.value =
                "Discovery process ended unexpectedly without a final result.";
              showError(fetchError.value);
              isDiscoveringSSE.value = false;
              resolve(null); // Resolve with null as discovery failed
            }
            break; // Exit loop
          }

          buffer += decoder.decode(value, { stream: true });
          const lines = buffer.split("\n");
          buffer = lines.pop() || ""; // Keep the last partial line

          lines.forEach((line) => {
            if (line.startsWith("event:")) {
              currentEventType = line.substring("event:".length).trim();
            } else if (line.startsWith("data:")) {
              const data = line.substring("data:".length).trim();
              try {
                const jsonData = JSON.parse(data);
                if (currentEventType === "log_entry") {
                  const logEntry = jsonData as DiscoveryLogEntry;
                  discoveryLogMap.set(logEntry.stepId, logEntry); // Update map reactively
                } else if (currentEventType === "final_result") {
                  const finalData = handleFinalResult(jsonData);
                  reader.cancel(); // Stop reading further
                  resolve(finalData); // Resolve the main promise with the final data
                  return; // Exit line processing
                }
              } catch (e) {
                console.error("Error parsing SSE data:", e, "Data:", data);
                // Optionally handle parse error (e.g., show snackbar)
              }
            } else if (line === "") {
              // Reset event type after processing a block
              currentEventType = "message";
            }
          });
        }
      } catch (error: unknown) {
        const message =
          error instanceof Error
            ? error.message
            : "Failed to initiate discovery stream.";
        fetchError.value = message; // Store the setup/fetch error
        showError(message);
        console.error("Error initiating/processing discovery SSE:", error);
        isDiscoveringSSE.value = false; // Ensure loading state is reset
        closeEventSource();
        reject(new Error(message)); // Reject the main promise
      }
    });
  }

  // Expose state and methods
  return {
    isDiscoveringSSE: readonly(isDiscoveringSSE),
    fetchError: readonly(fetchError), // Expose fetch error related to stream setup
    discoveryLogMap: readonly(discoveryLogMap),
    discoveredInfo: readonly(discoveredInfo),
    discoveredProtocol: readonly(discoveredProtocol),
    startDiscoverySSE,
    resetDiscoveryState,
    // Note: handleFinalResult is internal, caller uses the promise resolution
  };
}
