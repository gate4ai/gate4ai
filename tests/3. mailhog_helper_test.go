package tests

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// MailHog structures (simplified)
type MailHogMessage struct {
	ID      string `json:"ID"`
	To      []struct{ Mailbox, Domain string }
	From    struct{ Mailbox, Domain string }
	Content struct {
		Headers map[string][]string
		Body    string
		Size    int
		MIME    *MailHogMIMEPart // Can be nil if not multipart
	}
	Created string // Timestamp string
}

type MailHogMIMEPart struct {
	Parts   []*MailHogMIMEPart
	Headers map[string][]string
	Body    string
	Size    int
}

// fetchEmailsFromMailHog fetches emails from MailHog API, optionally filtering by recipient
func fetchEmailsFromMailHog(t *testing.T, recipientEmail string, since time.Time, timeout time.Duration) ([]MailHogMessage, error) {
	t.Helper()
	startTime := time.Now()
	var lastError error

	for time.Since(startTime) < timeout {
		client := &http.Client{Timeout: 5 * time.Second} // Short timeout for each request
		apiURL := fmt.Sprintf("%s/api/v2/messages", MAILHOG_API_URL)
		resp, err := client.Get(apiURL)
		if err != nil {
			lastError = fmt.Errorf("failed to connect to MailHog API: %w", err)
			time.Sleep(500 * time.Millisecond) // Wait before retrying connection errors
			continue
		}

		if resp.StatusCode != http.StatusOK {
			bodyBytes, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			lastError = fmt.Errorf("mailHog API returned status %d: %s", resp.StatusCode, string(bodyBytes))
			time.Sleep(500 * time.Millisecond) // Wait before retrying API errors
			continue
		}

		bodyBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastError = fmt.Errorf("failed to read MailHog response body: %w", err)
			time.Sleep(200 * time.Millisecond) // Wait before retrying read errors
			continue
		}

		var messagesResponse struct {
			Messages []MailHogMessage `json:"items"`
			Total    int              `json:"total"`
			Count    int              `json:"count"`
		}
		if err := json.Unmarshal(bodyBytes, &messagesResponse); err != nil {
			lastError = fmt.Errorf("failed to parse MailHog messages: %w body: %s", err, string(bodyBytes))
			time.Sleep(200 * time.Millisecond) // Wait before retrying parse errors
			continue
		}

		var matchingEmails []MailHogMessage
		for _, msg := range messagesResponse.Messages {
			msgCreatedTime, err := time.Parse(time.RFC3339, msg.Created)
			if err != nil {
				t.Logf("Warning: Could not parse message timestamp '%s': %v", msg.Created, err)
				continue // Skip message if timestamp is invalid
			}
			// Check timestamp first
			if msgCreatedTime.Before(since) {
				continue // Skip emails older than 'since'
			}

			// Check recipient
			recipientFound := false
			for _, to := range msg.To {
				fullAddress := fmt.Sprintf("%s@%s", to.Mailbox, to.Domain)
				if strings.EqualFold(fullAddress, recipientEmail) {
					recipientFound = true
					break
				}
			}

			if recipientFound {
				matchingEmails = append(matchingEmails, msg)
			}
		}

		if len(matchingEmails) > 0 {
			t.Logf("Found %d matching email(s) for %s since %s", len(matchingEmails), recipientEmail, since.Format(time.RFC3339))
			return matchingEmails, nil // Found matching emails
		}

		// No matching emails yet, wait a bit before polling again
		time.Sleep(500 * time.Millisecond)
	}

	if lastError != nil {
		return nil, fmt.Errorf("timed out waiting for email for %s. Last error: %w", recipientEmail, lastError)
	}
	return nil, fmt.Errorf("timed out waiting for email for %s", recipientEmail) // Timeout without errors
}

// findLinkInEmail searches the email body (HTML or plain text) for the first link matching a prefix
func findLinkInEmail(t *testing.T, msg MailHogMessage, linkPrefix string) (string, error) {
	t.Helper()
	// Simple search in the main body first
	bodyContent := msg.Content.Body
	link := findLink(bodyContent, linkPrefix)
	if link != "" {
		return link, nil
	}

	// If multipart, search through parts (basic recursive search)
	var searchParts func(parts []*MailHogMIMEPart) string
	searchParts = func(parts []*MailHogMIMEPart) string {
		if parts == nil {
			return ""
		}
		for _, part := range parts {
			partLink := findLink(part.Body, linkPrefix)
			if partLink != "" {
				return partLink
			}
			// Recursively search nested parts
			nestedLink := searchParts(part.Parts)
			if nestedLink != "" {
				return nestedLink
			}
		}
		return ""
	}

	if msg.Content.MIME != nil {
		mimeLink := searchParts(msg.Content.MIME.Parts)
		if mimeLink != "" {
			return mimeLink, nil
		}
	}

	return "", fmt.Errorf("link starting with '%s' not found in email body", linkPrefix)
}

// findLink is a helper to find the first occurrence of `href="<linkPrefix>..."`
func findLink(content, prefix string) string {
	// Preprocess the content to handle Quoted-Printable encoding
	// Replace soft line breaks (=\r\n) with empty string
	content = strings.ReplaceAll(content, "=\r\n", "")

	// Replace =3D with =
	content = strings.ReplaceAll(content, "=3D", "=")

	// Try standard href format
	hrefAttr := `href="` + prefix
	startIndex := strings.Index(content, hrefAttr)

	if startIndex == -1 {
		return ""
	}

	startIndex += len(`href="`) // Move index to the start of the actual URL

	// Find the closing quote
	endIndex := strings.Index(content[startIndex:], `"`)
	if endIndex == -1 {
		return "" // Malformed link
	}

	return content[startIndex : startIndex+endIndex]
}

// deleteAllMailHogMessages clears all emails from MailHog
func deleteAllMailHogMessages(t *testing.T) {
	t.Helper()
	apiURL := fmt.Sprintf("%s/api/v1/messages", MAILHOG_API_URL)
	req, err := http.NewRequest("DELETE", apiURL, nil)
	require.NoError(t, err)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode, "Failed to delete MailHog messages")
	t.Log("Deleted all messages from MailHog")
}
