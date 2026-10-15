package host

import (
	"strings"
	"testing"
)

func TestExternalIdenticalActiveProcedureReusesOriginalEvidence(t *testing.T) {
	h, c := externalHost(t)
	first := processedExternal(t, h, submitExternal(t, h, c, "original", true))
	if first.Results[0].Status != "published" {
		t.Fatal(first.Results)
	}
	id := first.Results[0].MemoryID
	h.Judge = externalJudge{eligible: false}
	repeat := processedExternal(t, h, submitExternal(t, h, c, "repeat", true))
	result := repeat.Results[0]
	if result.Status != "published" || result.Reason != "duplicate_existing_memory" || result.MemoryID != id || result.Revision != 1 || !strings.Contains(string(result.Audit), "incoming evidence not reassessed or merged") {
		t.Fatal("repeat depended on a new model vote", result)
	}
	m, err := h.Store.Memory(t.Context(), id)
	if err != nil || m.ExternalReceiptID != first.ID || m.Revision != 1 {
		t.Fatal("repeat rewrote original evidence", m, err)
	}
	changed := repeat.Submission
	changed.Key = "changed-content"
	changed.Items[0].Procedure.Summary += " It works for every future input."
	received, err := h.Store.ReceiveExternal(t.Context(), c.ID, ID(), changed)
	if err != nil {
		t.Fatal(err)
	}
	if got := processedExternal(t, h, received); got.Results[0].Status != "excluded" {
		t.Fatal("changed knowledge bypassed assessment", got.Results)
	}
	if err := h.Store.InvalidateMemory(t.Context(), id, "test withdrawal"); err != nil {
		t.Fatal(err)
	}
	if got := processedExternal(t, h, submitExternal(t, h, c, "withdrawn-repeat", true)); got.Results[0].Status != "excluded" {
		t.Fatal("withdrawn duplicate was readmitted", got.Results)
	}
	m, err = h.Store.Memory(t.Context(), id)
	if err != nil || m.Status != "invalidated" {
		t.Fatal("withdrawn source was revived", m, err)
	}
}
