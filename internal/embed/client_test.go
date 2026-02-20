package embed

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newMockOllamaServer returns a test server that responds to /api/embeddings
// and /api/generate with canned responses.
func newMockOllamaServer(t *testing.T, embedResp embeddingResponse, genResp generateResponse) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/embeddings":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(embedResp)
		case "/api/generate":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(genResp)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestEmbed_HappyPath(t *testing.T) {
	want := []float64{0.1, 0.2, 0.3, 0.4}
	srv := newMockOllamaServer(t, embeddingResponse{Embedding: want}, generateResponse{})
	defer srv.Close()

	c := NewClient(srv.URL, "test-model")
	got, err := c.Embed("hello world")
	if err != nil {
		t.Fatalf("Embed() error: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d dims, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("dim[%d]: want %f, got %f", i, want[i], got[i])
		}
	}
}

func TestEmbed_EmptyText(t *testing.T) {
	c := NewClient("http://localhost:11434", "test-model")
	_, err := c.Embed("")
	if err == nil {
		t.Fatal("expected error for empty text, got nil")
	}
}

func TestEmbed_CachesResult(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/embeddings" {
			callCount++
			json.NewEncoder(w).Encode(embeddingResponse{Embedding: []float64{1.0, 0.0}})
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-model")

	// Call twice with the same text.
	if _, err := c.Embed("same text"); err != nil {
		t.Fatalf("first Embed() error: %v", err)
	}
	if _, err := c.Embed("same text"); err != nil {
		t.Fatalf("second Embed() error: %v", err)
	}

	if callCount != 1 {
		t.Errorf("expected 1 HTTP call (cache hit), got %d", callCount)
	}
}

func TestEmbed_DifferentTextsDontShareCache(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/embeddings" {
			callCount++
			json.NewEncoder(w).Encode(embeddingResponse{Embedding: []float64{float64(callCount)}})
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-model")
	c.Embed("text one")
	c.Embed("text two")

	if callCount != 2 {
		t.Errorf("expected 2 HTTP calls for different texts, got %d", callCount)
	}
}

func TestEmbed_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-model")
	_, err := c.Embed("some text")
	if err == nil {
		t.Fatal("expected error for HTTP 500, got nil")
	}
}

func TestEmbed_EmptyEmbeddingResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(embeddingResponse{Embedding: nil})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-model")
	_, err := c.Embed("some text")
	if err == nil {
		t.Fatal("expected error for empty embedding response, got nil")
	}
}

func TestGenerate_HappyPath(t *testing.T) {
	srv := newMockOllamaServer(t, embeddingResponse{}, generateResponse{Response: "hello response", Done: true})
	defer srv.Close()

	c := NewClient(srv.URL, "test-model")
	got, err := c.Generate("some prompt")
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}
	if got != "hello response" {
		t.Errorf("expected 'hello response', got %q", got)
	}
}

func TestGenerate_EmptyPrompt(t *testing.T) {
	c := NewClient("http://localhost:11434", "test-model")
	_, err := c.Generate("")
	if err == nil {
		t.Fatal("expected error for empty prompt, got nil")
	}
}

// --- Pure math helpers ---

func TestCosineSimilarity_Identical(t *testing.T) {
	a := []float64{1.0, 0.0, 0.0}
	sim := CosineSimilarity(a, a)
	if sim < 0.999 || sim > 1.001 {
		t.Errorf("expected ~1.0 for identical vectors, got %f", sim)
	}
}

func TestCosineSimilarity_Orthogonal(t *testing.T) {
	a := []float64{1.0, 0.0}
	b := []float64{0.0, 1.0}
	sim := CosineSimilarity(a, b)
	if sim > 0.001 || sim < -0.001 {
		t.Errorf("expected ~0 for orthogonal vectors, got %f", sim)
	}
}

func TestCosineSimilarity_Opposite(t *testing.T) {
	a := []float64{1.0, 0.0}
	b := []float64{-1.0, 0.0}
	sim := CosineSimilarity(a, b)
	if sim > -0.999 {
		t.Errorf("expected ~-1 for opposite vectors, got %f", sim)
	}
}

func TestCosineSimilarity_DimMismatch(t *testing.T) {
	a := []float64{1.0, 0.0}
	b := []float64{1.0}
	sim := CosineSimilarity(a, b)
	if sim != 0 {
		t.Errorf("expected 0 for mismatched dims, got %f", sim)
	}
}

func TestCosineSimilarity_Empty(t *testing.T) {
	sim := CosineSimilarity(nil, nil)
	if sim != 0 {
		t.Errorf("expected 0 for nil vectors, got %f", sim)
	}
}

func TestAverageEmbeddings_Basic(t *testing.T) {
	embeddings := [][]float64{
		{1.0, 0.0},
		{0.0, 1.0},
	}
	avg := AverageEmbeddings(embeddings)
	if len(avg) != 2 {
		t.Fatalf("expected len 2, got %d", len(avg))
	}
	if avg[0] != 0.5 || avg[1] != 0.5 {
		t.Errorf("expected [0.5, 0.5], got %v", avg)
	}
}

func TestAverageEmbeddings_Empty(t *testing.T) {
	avg := AverageEmbeddings(nil)
	if avg != nil {
		t.Errorf("expected nil for empty input, got %v", avg)
	}
}

func TestUpdateCentroid_BlendCorrectly(t *testing.T) {
	current := []float64{1.0, 0.0}
	newEmb := []float64{0.0, 1.0}
	alpha := 0.5

	result := UpdateCentroid(current, newEmb, alpha)
	if len(result) != 2 {
		t.Fatalf("expected len 2, got %d", len(result))
	}
	// With alpha=0.5: result = 0.5*new + 0.5*current = [0.5, 0.5]
	if result[0] != 0.5 || result[1] != 0.5 {
		t.Errorf("expected [0.5, 0.5], got %v", result)
	}
}

func TestUpdateCentroid_EmptyCurrent(t *testing.T) {
	newEmb := []float64{1.0, 2.0}
	result := UpdateCentroid(nil, newEmb, 0.5)
	if len(result) != len(newEmb) {
		t.Fatalf("expected len %d, got %d", len(newEmb), len(result))
	}
	for i, v := range result {
		if v != newEmb[i] {
			t.Errorf("dim[%d]: expected %f, got %f", i, newEmb[i], v)
		}
	}
}

func TestUpdateCentroid_DimMismatch(t *testing.T) {
	current := []float64{1.0}
	newEmb := []float64{0.0, 1.0}
	result := UpdateCentroid(current, newEmb, 0.5)
	// Should fall back to newEmb on mismatch
	if len(result) != len(newEmb) {
		t.Errorf("expected fallback to newEmb, got len %d", len(result))
	}
}

// embeddingCache unit tests

func TestEmbeddingCache_SetGet(t *testing.T) {
	c := newEmbeddingCache(2)
	c.set("k1", []float64{1.0, 2.0})

	v, ok := c.get("k1")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if v[0] != 1.0 || v[1] != 2.0 {
		t.Errorf("unexpected cached value: %v", v)
	}
}

func TestEmbeddingCache_Eviction(t *testing.T) {
	c := newEmbeddingCache(2)
	c.set("k1", []float64{1.0})
	c.set("k2", []float64{2.0})
	c.set("k3", []float64{3.0}) // evicts k1

	if _, ok := c.get("k1"); ok {
		t.Error("expected k1 to be evicted")
	}
	if _, ok := c.get("k2"); !ok {
		t.Error("expected k2 to still be present")
	}
	if _, ok := c.get("k3"); !ok {
		t.Error("expected k3 to be present")
	}
}

func TestEmbeddingCache_Miss(t *testing.T) {
	c := newEmbeddingCache(10)
	_, ok := c.get("nonexistent")
	if ok {
		t.Error("expected cache miss")
	}
}
