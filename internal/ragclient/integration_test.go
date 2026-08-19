package ragclient_test

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
)

func TestIntegrationRetrieve(t *testing.T) {
	if os.Getenv("RAG_INTEGRATION") != "1" {
		t.Skip("set RAG_INTEGRATION=1 to run against a real RAG service")
	}
	kbID, err := strconv.ParseUint(os.Getenv("RAG_INTEGRATION_KB_ID"), 10, 64)
	if err != nil || kbID == 0 {
		t.Fatal("RAG_INTEGRATION_KB_ID must be a positive integer")
	}
	client, err := ragclient.NewClient(ragclient.ClientConfig{
		BaseURL: os.Getenv("RAG_BASE_URL"),
		APIKey:  os.Getenv("RAG_API_KEY"),
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	result, err := client.Retrieve(context.Background(), ragclient.RetrieveRequest{
		Query: "垃圾回收",
		KBIDs: []uint64{kbID},
		TopK:  5,
	})
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if result.RequestID == "" {
		t.Fatal("RAG response has no request_id")
	}
	if result.Refusal != nil {
		t.Fatalf("RAG refused integration query: %s", result.Refusal.Message)
	}
	if len(result.Items) == 0 {
		t.Fatal("RAG returned no items for garbage collection query")
	}
	for index, item := range result.Items {
		if item.Citation.KBID != kbID || item.Citation.FileName == "" || item.Citation.ChunkID == "" {
			t.Fatalf("item %d has incomplete citation: %#v", index, item.Citation)
		}
	}
}
