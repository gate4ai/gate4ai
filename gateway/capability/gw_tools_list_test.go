package capability_test

import (
	"testing"

	"github.com/gate4ai/gate4ai/tests"
	"go.uber.org/zap"
)

func TestGetToolsList(t *testing.T) {
	list, err := tests.GetToolsList(GW_URL, "key-user1", LOGGER.With(zap.String("s", "TestGetToolsList1")))
	if err != nil {
		t.Fatalf("Failed to get tools list: %v", err)
	}
	t.Logf("user1 tools list: %v", list)
	if len(list) != 6 {
		t.Fatalf("No tools found")
	}
	list, err = tests.GetToolsList(GW_URL, "key-user2", LOGGER.With(zap.String("s", "TestGetToolsList2")))
	if err != nil {
		t.Fatalf("Failed to get tools list: %v", err)
	}
	t.Logf("user2 tools list: %v", list)
	if len(list) != 6 {
		t.Fatalf("No tools found")
	}
	list, err = tests.GetToolsList(GW_URL, "key-user3", LOGGER.With(zap.String("s", "TestGetToolsList3")))
	if err != nil {
		t.Fatalf("Failed to get tools list: %v", err)
	}
	t.Logf("user3 tools list: %v", list)
	if len(list) != 12 {
		t.Fatalf("No tools found")
	}
}
