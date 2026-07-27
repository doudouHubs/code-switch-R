package services

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func boolPtr(value bool) *bool {
	return &value
}

func floatPtr(value float64) *float64 {
	return &value
}

func TestRadarServiceGetSnapshotAggregatesOriginalTableRules(t *testing.T) {
	payload := radarTablePayload{
		Combos: []radarCombo{
			{Model: "gpt-5.6-sol", Effort: "high"},
			{Model: "gpt-5.6-terra", Effort: "ultra"},
		},
		Tasks: []radarTask{{ID: "task-a"}, {ID: "task-b"}},
		Cells: map[string]radarCell{
			radarCellKey("task-a", "gpt-5.6-sol", "high"): {
				RanBy: []radarRun{{Passed: boolPtr(true), DurationSec: floatPtr(600), ActualCostUSD: floatPtr(2), GradedAt: "2026-07-27T01:00:00Z"}},
			},
			radarCellKey("task-b", "gpt-5.6-sol", "high"): {
				RanBy: []radarRun{{Passed: boolPtr(false), DurationSec: floatPtr(1200), ActualCostUSD: floatPtr(4), GradedAt: "2026-07-27T02:00:00Z"}},
			},
			radarCellKey("task-a", "gpt-5.6-terra", "ultra"): {
				RanBy: []radarRun{{Passed: boolPtr(true), DurationSec: floatPtr(1800), ActualCostUSD: floatPtr(7), CostComplete: true, GradedAt: "2026-07-27T03:00:00Z"}},
			},
			radarCellKey("task-b", "gpt-5.6-terra", "ultra"): {
				RanBy: []radarRun{{Passed: boolPtr(true), DurationSec: floatPtr(2400), ActualCostUSD: floatPtr(1), CostComplete: false, GradedAt: "2026-07-27T04:00:00Z"}},
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Fatalf("Accept = %q, want application/json", r.Header.Get("Accept"))
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Fatalf("encode payload: %v", err)
		}
	}))
	defer server.Close()

	snapshot, err := newRadarService(server.URL, server.Client()).GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot() error = %v", err)
	}
	if snapshot.SourceUpdatedAt != "2026-07-27T04:00:00Z" {
		t.Fatalf("SourceUpdatedAt = %q", snapshot.SourceUpdatedAt)
	}
	if len(snapshot.Points) != 2 {
		t.Fatalf("points = %d, want 2", len(snapshot.Points))
	}

	sol := snapshot.Points[0]
	if sol.Passed != 1 || sol.ValidTasks != 2 || sol.IQ != 75 {
		t.Fatalf("Sol aggregation = %+v, want passed 1/2 and IQ 75", sol)
	}
	if sol.AveragePriceUSD == nil || *sol.AveragePriceUSD != 3 {
		t.Fatalf("Sol average price = %v, want 3", sol.AveragePriceUSD)
	}
	if sol.AverageMinutes == nil || *sol.AverageMinutes != 15 {
		t.Fatalf("Sol average minutes = %v, want 15", sol.AverageMinutes)
	}

	ultra := snapshot.Points[1]
	if ultra.IQ != 150 || ultra.PriceSamples != 1 {
		t.Fatalf("Ultra aggregation = %+v, want IQ 150 and one complete cost sample", ultra)
	}
	if ultra.AveragePriceUSD == nil || *ultra.AveragePriceUSD != 7 {
		t.Fatalf("Ultra average price = %v, want 7 after incomplete cost exclusion", ultra.AveragePriceUSD)
	}
	if sol.CombinedCostIndex == nil || ultra.CombinedCostIndex == nil {
		t.Fatal("combined cost index should be present for both points")
	}
	if math.Abs(*sol.CombinedCostIndex-3.22479808770649) > 0.000001 {
		t.Fatalf("sol combined cost index = %f, want source-formula value", *sol.CombinedCostIndex)
	}
	if math.Abs(*ultra.CombinedCostIndex-100) > 0.000001 {
		t.Fatalf("ultra combined cost index = %f, want 100", *ultra.CombinedCostIndex)
	}
}

func TestAggregateRadarTableRejectsMissingRequiredCollections(t *testing.T) {
	if _, err := aggregateRadarTable(radarTablePayload{}); err == nil {
		t.Fatal("aggregateRadarTable() error = nil, want validation failure")
	}
}
