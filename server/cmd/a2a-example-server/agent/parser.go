package agent

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	a2aSchema "github.com/gate4ai/gate4ai/shared/a2a/2025-draft/schema"
	"go.uber.org/zap"
)

// Regular expressions for parsing commands - case insensitive (?i)
var (
	waitRegex       = regexp.MustCompile(`(?i)\bwait\s+(\d+)\s+seconds?\b`)
	askRegex        = regexp.MustCompile(`(?i)\bask\s+for\s+input(?:\s+(?:"([^"]*)"|'([^']*)'|(\S+)))?\b`)
	streamRegex     = regexp.MustCompile(`(?i)\bstream\s+(\d+)\s+chunks?\b`)
	errorRegex      = regexp.MustCompile(`(?i)\btrigger\s+error\s+(-?\d+|fail)\b`)
	respondRegex    = regexp.MustCompile(`(?i)\brespond\s+(?:with\s+)?(text|file|data)\s*(.*)`)
	getHeadersRegex = regexp.MustCompile(`(?i)\bget_headers\b`)       // NEW: Regex for get_headers command
	quotedStrRegex  = regexp.MustCompile(`^(?:"([^"]*)"|'([^']*)')$`) // For quoted payloads like 'payload' or "payload"
)

// parseCommandsFromMessage extracts an ordered list of commands from all parts of a message.
// It processes text parts for commands and treats file/data parts as implicit "respond text" commands providing context.
func parseCommandsFromMessage(msg a2aSchema.Message, logger *zap.Logger) ([]ParsedCommand, string) {
	commands := make([]ParsedCommand, 0)
	firstTextPartContent := ""
	foundFirstText := false
	var combinedText strings.Builder // Combine text from all text parts

	// First pass: extract text and handle file/data parts as implicit commands
	for _, part := range msg.Parts {
		partType := "text" // Default to text if type is missing or unknown
		if part.Type != nil {
			partType = *part.Type
		}

		switch partType {
		case "text":
			if part.Text != nil {
				text := *part.Text
				combinedText.WriteString(text + " ") // Add space separator
				if !foundFirstText {
					firstTextPartContent = text
					foundFirstText = true
				}
			}
		case "file":
			// Treat incoming file as context, generate a "respond text" command with file details
			fileName := "input_file"
			mimeType := "application/octet-stream"
			if part.File != nil {
				if part.File.Name != nil {
					fileName = *part.File.Name
				}
				if part.File.MimeType != nil {
					mimeType = *part.File.MimeType
				}
				// We don't include bytes in the response text, just metadata
			}
			responseText := fmt.Sprintf("Received file part: name='%s', mimeType='%s'", fileName, mimeType)
			commands = append(commands, ParsedCommand{
				Type: "respond",
				Params: map[string]interface{}{
					"respondType": "text",
					"payload":     responseText,
				},
			})
			logger.Debug("Generated implicit 'respond text' for FilePart", zap.String("fileName", fileName), zap.String("mimeType", mimeType))

		case "data":
			// Treat incoming data as context, generate a "respond text" command with data details
			dataStr := "{}" // Default empty JSON
			if part.Data != nil {
				jsonData, err := json.Marshal(part.Data)
				if err == nil {
					dataStr = string(jsonData)
				} else {
					logger.Error("Failed to marshal DataPart for implicit response", zap.Error(err))
					dataStr = "[Error marshalling data]"
				}
			}
			responseText := fmt.Sprintf("Received data part: %s", dataStr)
			commands = append(commands, ParsedCommand{
				Type: "respond",
				Params: map[string]interface{}{
					"respondType": "text",
					"payload":     responseText,
				},
			})
			logger.Debug("Generated implicit 'respond text' for DataPart")

		default:
			logger.Warn("Unknown message part type encountered during command parsing", zap.String("type", partType))
		}
	}

	// Second pass: Parse commands from the combined text content
	fullText := strings.TrimSpace(combinedText.String())
	currentIndex := 0

	for currentIndex < len(fullText) {
		remainingText := fullText[currentIndex:]
		foundCmd := false
		nextIndex := len(fullText) // Default to end if no command found

		// --- Check for specific commands ---
		// Find the earliest occurrence of any command
		minIndex := -1

		updateMinIndex := func(matches []int) {
			if len(matches) > 0 && (minIndex == -1 || matches[0] < minIndex) {
				minIndex = matches[0]
			}
		}

		waitMatches := waitRegex.FindStringSubmatchIndex(remainingText)
		updateMinIndex(waitMatches)
		askMatches := askRegex.FindStringSubmatchIndex(remainingText)
		updateMinIndex(askMatches)
		streamMatches := streamRegex.FindStringSubmatchIndex(remainingText)
		updateMinIndex(streamMatches)
		errorMatches := errorRegex.FindStringSubmatchIndex(remainingText)
		updateMinIndex(errorMatches)
		respondMatches := respondRegex.FindStringSubmatchIndex(remainingText)
		updateMinIndex(respondMatches)
		getHeadersMatches := getHeadersRegex.FindStringSubmatchIndex(remainingText) // NEW
		updateMinIndex(getHeadersMatches)                                           // NEW

		// If no command found in the remaining text, break
		if minIndex == -1 {
			break
		}

		// Determine which command matched at minIndex
		absoluteMinIndex := currentIndex + minIndex

		if getHeadersMatches != nil && getHeadersMatches[0] == minIndex { // NEW: Handle get_headers
			commands = append(commands, ParsedCommand{Type: "get_headers", Params: map[string]interface{}{}})
			nextIndex = absoluteMinIndex + len(remainingText[getHeadersMatches[0]:getHeadersMatches[1]]) // Move past 'get_headers'
			foundCmd = true
		} else if waitMatches != nil && waitMatches[0] == minIndex {
			delayStr := remainingText[waitMatches[2]:waitMatches[3]]
			delay, _ := strconv.Atoi(delayStr) // Error ignored as regex ensures digits
			commands = append(commands, ParsedCommand{Type: "wait", Params: map[string]interface{}{"duration": delay}})
			nextIndex = absoluteMinIndex + len(remainingText[waitMatches[0]:waitMatches[1]]) // Move past the matched command
			foundCmd = true
		} else if askMatches != nil && askMatches[0] == minIndex {
			prompt := ""
			promptEndIndexInRemaining := askMatches[1] // End index within remainingText initially
			// Check which capture group matched for the prompt text
			if askMatches[2] != -1 { // Double quoted
				prompt = remainingText[askMatches[2]:askMatches[3]]
				promptEndIndexInRemaining = askMatches[3] + 1 // Position after closing quote
			} else if askMatches[4] != -1 { // Single quoted
				prompt = remainingText[askMatches[4]:askMatches[5]]
				promptEndIndexInRemaining = askMatches[5] + 1 // Position after closing quote
			} else if askMatches[6] != -1 { // Unquoted word/phrase until next command/end
				// Find end of unquoted prompt: either end of string or start of next command
				relativeStartIndex := askMatches[6]
				promptEndRel := len(remainingText)                         // Assume end of string initially
				tempRemainingAfterKeyword := remainingText[askMatches[1]:] // Text after "ask for input "
				if nextCmdRelIdx := findNextCommandIndex(tempRemainingAfterKeyword); nextCmdRelIdx != -1 {
					// Found next command, prompt ends before it
					promptEndRel = askMatches[1] + nextCmdRelIdx
				}
				// Ensure we don't go past the actual end of the remainingText string
				if promptEndRel > len(remainingText) {
					promptEndRel = len(remainingText)
				}
				// Extract the prompt, ensuring start index isn't out of bounds
				if relativeStartIndex < promptEndRel {
					prompt = strings.TrimSpace(remainingText[relativeStartIndex:promptEndRel])
					promptEndIndexInRemaining = promptEndRel // Update end index
				} else {
					prompt = ""                               // Handle edge case where indices might be invalid
					promptEndIndexInRemaining = askMatches[1] // Fallback
				}
			}
			commands = append(commands, ParsedCommand{Type: "ask", Params: map[string]interface{}{"prompt": prompt}})
			nextIndex = currentIndex + promptEndIndexInRemaining // Move index past the consumed part
			foundCmd = true
		} else if streamMatches != nil && streamMatches[0] == minIndex {
			countStr := remainingText[streamMatches[2]:streamMatches[3]]
			count, _ := strconv.Atoi(countStr)
			commands = append(commands, ParsedCommand{Type: "stream", Params: map[string]interface{}{"count": count}})
			nextIndex = absoluteMinIndex + len(remainingText[streamMatches[0]:streamMatches[1]])
			foundCmd = true
		} else if errorMatches != nil && errorMatches[0] == minIndex {
			codeOrType := remainingText[errorMatches[2]:errorMatches[3]]
			params := map[string]interface{}{}
			if code, err := strconv.Atoi(codeOrType); err == nil {
				params["code"] = code
			} else {
				params["type"] = strings.ToLower(codeOrType) // Assume "fail" or similar
			}
			commands = append(commands, ParsedCommand{Type: "error", Params: params})
			nextIndex = absoluteMinIndex + len(remainingText[errorMatches[0]:errorMatches[1]])
			foundCmd = true
		} else if respondMatches != nil && respondMatches[0] == minIndex {
			respondType := strings.ToLower(remainingText[respondMatches[2]:respondMatches[3]])
			payloadRaw := remainingText[respondMatches[4]:respondMatches[5]] // Potential payload + subsequent text
			payload := ""
			payloadEndIndexInRaw := 0 // Index within payloadRaw where the payload ends

			// Try matching quoted payload first
			if qMatches := quotedStrRegex.FindStringSubmatch(payloadRaw); qMatches != nil && strings.HasPrefix(payloadRaw, qMatches[0]) {
				if qMatches[1] != "" { // double quoted
					payload = qMatches[1]
				} else if qMatches[2] != "" { // single quoted
					payload = qMatches[2]
				}
				payloadEndIndexInRaw = len(qMatches[0]) // Length of the quoted string including quotes
			} else if strings.HasPrefix(strings.TrimSpace(payloadRaw), "{") && respondType == "data" {
				// Attempt to find the extent of the JSON object payload
				decoder := json.NewDecoder(strings.NewReader(payloadRaw))
				var js json.RawMessage
				if err := decoder.Decode(&js); err == nil {
					// Successfully decoded a JSON object/array
					payload = string(js)
					// Calculate how much of the input string was consumed by the decoder
					bytesRead := decoder.InputOffset()
					payloadEndIndexInRaw = int(bytesRead)
				} else {
					// Not a valid JSON start, or invalid JSON - treat as regular text? Or error?
					// Let's treat as unquoted text for now.
					nextCmdIdx := findNextCommandIndex(payloadRaw)
					if nextCmdIdx != -1 {
						payload = strings.TrimSpace(payloadRaw[:nextCmdIdx])
						payloadEndIndexInRaw = nextCmdIdx
					} else {
						payload = strings.TrimSpace(payloadRaw)
						payloadEndIndexInRaw = len(payloadRaw)
					}
					logger.Debug("Could not parse payload as JSON for 'respond data', treating as text.", zap.String("payloadRaw", payloadRaw))
				}
			} else {
				// Assume unquoted payload extends until the next command or end of string
				nextCmdIdx := findNextCommandIndex(payloadRaw)
				if nextCmdIdx != -1 {
					payload = strings.TrimSpace(payloadRaw[:nextCmdIdx])
					payloadEndIndexInRaw = nextCmdIdx
				} else {
					payload = strings.TrimSpace(payloadRaw)
					payloadEndIndexInRaw = len(payloadRaw)
				}
			}

			cmdParams := map[string]interface{}{"respondType": respondType, "payload": payload}
			validPayload := true
			if respondType == "data" && payload != "" {
				var js json.RawMessage
				if json.Unmarshal([]byte(payload), &js) != nil {
					// This case might occur if parsing logic above failed to grab full JSON
					logger.Warn("Invalid JSON payload for 'respond data'", zap.String("payload", payload))
					validPayload = false
					cmdParams["error"] = "Invalid JSON payload"
				}
			} else if respondType == "file" && payload != "" {
				_, err := base64.StdEncoding.DecodeString(payload)
				if err != nil && len(payload) > 0 {
					logger.Warn("Payload for 'respond file' may not be base64", zap.String("payload", payload))
					cmdParams["warning"] = "Payload is not valid base64 (or empty)"
				}
			}

			if validPayload {
				commands = append(commands, ParsedCommand{Type: "respond", Params: cmdParams})
			} else {
				logger.Error("Skipping invalid respond command", zap.Any("params", cmdParams))
			}

			// Calculate how much of the original string the respond command consumed
			// Base match: "respond with type " -> respondMatches[4] points to start of raw payload part
			// Consumed part: The part before payload + the length of the parsed payload within raw
			consumedLength := (respondMatches[4] - respondMatches[0]) + payloadEndIndexInRaw
			nextIndex = absoluteMinIndex + consumedLength
			foundCmd = true
		}

		// If a command was found, update currentIndex, otherwise break
		if foundCmd {
			currentIndex = nextIndex
		} else {
			break // Should not happen if minIndex != -1, but safety break
		}
	}

	return commands, firstTextPartContent
}

// findNextCommandIndex finds the starting index of the *next* command keyword within a string.
// Returns -1 if no command keyword is found.
func findNextCommandIndex(text string) int {
	minIndex := -1

	checkIndex := func(match []int) {
		// match[0] is the start index of the keyword within text
		if len(match) > 0 && match[0] != -1 && (minIndex == -1 || match[0] < minIndex) {
			minIndex = match[0]
		}
	}

	checkIndex(waitRegex.FindStringIndex(text))
	checkIndex(askRegex.FindStringIndex(text))
	checkIndex(streamRegex.FindStringIndex(text))
	checkIndex(errorRegex.FindStringIndex(text))
	checkIndex(respondRegex.FindStringIndex(text))
	checkIndex(getHeadersRegex.FindStringIndex(text)) // NEW

	return minIndex
}
