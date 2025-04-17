package tests

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"
	"github.com/shirou/gopsutil/v3/process"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// Global timestamp for the entire test run
var (
	testRunTimestamp   string
	testRunTimestampMu sync.Mutex
	onceTimestamp      sync.Once
)

// getTestRunTimestamp returns the shared timestamp for the entire test run
func getTestRunTimestamp() string {
	onceTimestamp.Do(func() {
		testRunTimestampMu.Lock()
		defer testRunTimestampMu.Unlock()

		testRunTimestamp = time.Now().Format("20060102150405")
	})
	return testRunTimestamp
}

// ArtifactManager handles the creation and storage of test artifacts
type ArtifactManager struct {
	Logger      *zap.Logger
	BaseDir     string
	TestName    string
	Timestamp   string
	ArtifactDir string
	T           *testing.T
	Browser     playwright.Browser
	Context     playwright.BrowserContext
	Page        playwright.Page
}

func isDebugMode() bool {
	pid := int32(os.Getppid())
	parentProc, err := process.NewProcess(pid)
	if err != nil {
		log.Printf("Error getting parent process: %v", err)
		return false
	}

	parentName, err := parentProc.Name()
	if err != nil {
		log.Printf("Error getting parent process name: %v", err)
		return false
	}

	// Common debugger process names
	debuggers := []string{"dlv", "debug"}
	for _, dbg := range debuggers {
		if parentName == dbg {
			return true
		}
	}

	// Check command line arguments for delve flags
	parentCmdline, err := parentProc.CmdlineSlice()
	if err == nil {
		for _, arg := range parentCmdline {
			if arg == "debug" || arg == "--" || strings.Contains(arg, "dlv") {
				return true
			}
		}
	}

	return false
}

func NewArtifactManager(t *testing.T) *ArtifactManager {
	t.Helper()

	timestamp := getTestRunTimestamp()
	testName := t.Name()

	baseDir := filepath.Join(TEST_CONFIG_WORKSPACE_FOLDER, "tests", "artifacts")

	runArtifactDir := filepath.Join(baseDir, timestamp)
	if err := os.MkdirAll(runArtifactDir, 0755); err != nil {
		t.Logf("Failed to create run artifact directory: %v", err)
		t.Fatalf("Failed to create run artifact directory: %v", err)
		return nil
	}

	// Create the artifact directory for this specific test
	artifactDir := filepath.Join(runArtifactDir, testName)
	if err := os.MkdirAll(artifactDir, 0755); err != nil {
		t.Logf("Failed to create test artifact directory: %v", err)
		t.Fatalf("Failed to create test artifact directory: %v", err)
		return nil
	}

	// Use zap.NewDevelopment for logging
	logger := zaptest.NewLogger(t)

	// Check if we're in debug mode
	dm := isDebugMode()

	// Launch browser using the global pw instance
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(!dm),
	})
	if err != nil {
		logger.Sync()
		t.Fatalf("could not launch browser: %v", err)
		return nil
	}

	// --- Playwright Isolation ---
	context, err := browser.NewContext(playwright.BrowserNewContextOptions{
		BaseURL: playwright.String(PORTAL_URL),
	})
	if err != nil {
		browser.Close()
		logger.Sync() // Ensure logs are written before fatal
		t.Fatalf("could not create browser context: %v", err)
	}

	page, err := context.NewPage()
	if err != nil {
		context.Close() // Clean up context if page creation fails
		browser.Close()
		logger.Sync()
		t.Fatalf("could not create page: %v", err)
	}

	page.SetDefaultNavigationTimeout(30000)
	page.SetDefaultTimeout(30000)
	// --- End Playwright Isolation ---

	am := &ArtifactManager{
		Logger:      logger,
		BaseDir:     baseDir,
		TestName:    testName,
		Timestamp:   timestamp,
		ArtifactDir: artifactDir,
		T:           t,
		Browser:     browser,
		Context:     context,
		Page:        page,
	}

	am.SetupConsoleLogging()

	return am
}

func (am *ArtifactManager) Close() {
	if am == nil {
		return
	}
	if am.Context != nil {
		if err := am.Context.Close(); err != nil {
			// Логируем ошибку, но не прерываем выполнение
			am.T.Logf("Error closing playwright context: %v", err)
		}
	}
	if am.Browser != nil {
		if err := am.Browser.Close(); err != nil {
			am.T.Logf("Error closing browser: %v", err)
		}
	}
	if am.Logger != nil {
		am.Logger.Sync()
	}
}

// SaveScreenshot takes a screenshot and saves it to the artifacts directory
func (am *ArtifactManager) SaveScreenshot(name string) string {
	if am == nil || am.Page == nil {
		if am != nil && am.T != nil {
			am.T.Logf("Artifact manager or page is nil, cannot save screenshot")
		}
		return ""
	}

	filename := filepath.Join(am.ArtifactDir, fmt.Sprintf("%s.png", name))

	_, err := am.Page.Screenshot(playwright.PageScreenshotOptions{
		Path:     playwright.String(filename),
		FullPage: playwright.Bool(true),
	})

	if err != nil {
		am.T.Logf("Failed to save screenshot: %v", err)
		return ""
	}

	am.T.Logf("Screenshot saved to %s", filename)
	return filename
}

// SaveHTML saves the page HTML to the artifacts directory
func (am *ArtifactManager) SaveHTML(name string) string {
	if am == nil || am.Page == nil {
		if am != nil && am.T != nil {
			am.T.Logf("Artifact manager or page is nil, cannot save HTML")
		}
		return ""
	}

	filename := filepath.Join(am.ArtifactDir, fmt.Sprintf("%s.html", name))

	content, err := am.Page.Content()
	if err != nil {
		am.T.Logf("Failed to get page content: %v", err)
		return ""
	}

	err = os.WriteFile(filename, []byte(content), 0644)
	if err != nil {
		am.T.Logf("Failed to save HTML content: %v", err)
		return ""
	}

	am.T.Logf("HTML content saved to %s", filename)
	return filename
}

// SaveLocatorDebugInfo saves debug information when a locator is not found
func (am *ArtifactManager) SaveLocatorDebugInfo(selector string, description string) {
	if am == nil || am.Page == nil {
		if am != nil && am.T != nil {
			am.T.Logf("Artifact manager or page is nil, cannot save locator debug info")
		}
		return
	}

	// Take a screenshot of the current state
	am.SaveScreenshot(fmt.Sprintf("locator_not_found_%s", description))

	// Save the page HTML
	am.SaveHTML(fmt.Sprintf("locator_not_found_%s", description))

	// Save additional debug info
	filename := filepath.Join(am.ArtifactDir, fmt.Sprintf("locator_debug_%s.txt", description))
	debugInfo := fmt.Sprintf("Selector: %s\nDescription: %s\nURL: %s\nTimestamp: %s\n",
		selector, description, am.Page.URL(), time.Now().Format(time.RFC3339))

	err := os.WriteFile(filename, []byte(debugInfo), 0644)
	if err != nil {
		am.T.Logf("Failed to save debug info: %v", err)
		return
	}

	am.T.Logf("Locator debug info saved to %s", filename)
}

// WaitForLocatorWithDebug waits for a locator and saves debug info if not found
func (am *ArtifactManager) WaitForLocatorWithDebug(selector string, description string) (playwright.Locator, error) {
	if am == nil || am.Page == nil {
		if am != nil && am.T != nil {
			am.T.Logf("Artifact manager or page is nil, cannot wait for locator")
		}
		return nil, fmt.Errorf("artifact manager or page is nil")
	}

	locator := am.Page.Locator(selector)
	err := locator.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(10000),
	})

	if err != nil {
		am.T.Logf("Locator not found: %s - %s", selector, description)
		am.SaveLocatorDebugInfo(selector, description)
		return nil, err
	}

	return locator.First(), nil
}

// ClickWithDebug attempts to click a locator and saves debug info if it fails
func (am *ArtifactManager) ClickWithDebug(selector string, description string) error {
	if am == nil || am.Page == nil {
		if am != nil && am.T != nil {
			am.T.Logf("Artifact manager or page is nil, cannot click")
		}
		return fmt.Errorf("artifact manager or page is nil")
	}

	locator := am.Page.Locator(selector)
	err := locator.Click()

	if err != nil {
		am.T.Logf("Failed to click locator: %s - %s", selector, description)
		am.SaveLocatorDebugInfo(selector, description)
		return err
	}

	return nil
}

// FillWithDebug attempts to fill a form field and saves debug info if it fails
func (am *ArtifactManager) FillWithDebug(selector string, value string, description string) error {
	if am == nil || am.Page == nil {
		if am != nil && am.T != nil {
			am.T.Logf("Artifact manager or page is nil, cannot fill field")
		}
		return fmt.Errorf("artifact manager or page is nil")
	}

	locator := am.Page.Locator(selector)
	err := locator.Fill(value)

	if err != nil {
		am.T.Logf("Failed to fill locator: %s - %s", selector, description)
		am.SaveLocatorDebugInfo(selector, description)
		return err
	}

	return nil
}

func (am *ArtifactManager) SetupConsoleLogging() {
	if am == nil || am.Page == nil {
		if am != nil && am.T != nil {
			am.T.Logf("Artifact manager or page is nil, cannot setup console logging")
		}
		return
	}

	logFile := filepath.Join(am.ArtifactDir, "console.log")
	file, err := os.Create(logFile)
	if err != nil {
		am.T.Logf("Failed to create console log file: %v", err)
		return
	}

	am.Page.On("console", func(msg playwright.ConsoleMessage) {
		logEntry := fmt.Sprintf("[%s] [%s] %s\n",
			time.Now().Format(time.RFC3339),
			msg.Type(),
			msg.Text())

		var logMu sync.Mutex
		logMu.Lock()
		defer logMu.Unlock()
		if _, writeErr := file.WriteString(logEntry); writeErr != nil {
			fmt.Fprintf(os.Stderr, "[Console Logger Error] Failed to write to console log for test %s: %v\n", am.TestName, writeErr)
		}
	})

	am.Context.On("close", func() {
		file.Close()
		am.T.Logf("Closed console log file: %s", logFile)
	})

	am.T.Logf("Console logging set up to %s", logFile)
}

func (am *ArtifactManager) OpenPageWithURL(path string) {
	if am == nil || am.Page == nil {
		if am != nil && am.T != nil {
			am.T.Fatalf("ArtifactManager or Page is nil, cannot navigate.")
		} else {
			log.Fatalf("ArtifactManager or Page is nil in OpenPageWithURL")
		}
		return
	}

	debugName := fmt.Sprintf("page_navigation_%s", strings.ReplaceAll(path, "/", "_"))

	am.T.Logf("Navigating page [%s] to %s", am.TestName, path)
	response, err := am.Page.Goto(path, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateNetworkidle, // Wait until network is idle
		Timeout:   playwright.Float(30000),              // 30 seconds timeout
	})

	if err != nil {
		am.SaveScreenshot(debugName + "_nav_error")
		am.SaveHTML(debugName + "_nav_error")
		am.T.Fatalf("could not navigate page [%s] to %s: %v", am.TestName, path, err)
	}

	// Log status code
	if response != nil {
		am.T.Logf("Page [%s] loaded %s with status: %d", am.TestName, path, response.Status())
		// Check for client-side errors (e.g., 4xx, 5xx loaded into the page)
		if response.Status() >= 400 {
			am.SaveScreenshot(debugName + "_status_error")
			am.SaveHTML(debugName + "_status_error")
			am.T.Errorf("Page [%s] loaded with error status %d for URL %s", am.TestName, response.Status(), path)
		}
	} else {
		am.T.Logf("Page [%s] navigated to %s, but response was nil (possible client-side redirect?)", am.TestName, path)
	}

	// Wait for the page content to be *loaded* (DOM ready), networkidle уже был
	if err := am.Page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State:   playwright.LoadStateLoad, // 'load' or 'domcontentloaded'
		Timeout: playwright.Float(15000),  // Уменьшаем таймаут здесь
	}); err != nil {
		am.T.Logf("Page [%s] load state timeout for %s: %v", am.TestName, path, err)
		am.SaveScreenshot(debugName + "_load_timeout")
		am.SaveHTML(debugName + "_load_timeout")
		// Логируем как предупреждение, а не фатальную ошибку
		am.T.Logf("Warning: Page load state timeout for %s, continuing test cautiously.", path)
	}

	// Save a successful navigation screenshot
	am.SaveScreenshot(debugName + "_success")
}
