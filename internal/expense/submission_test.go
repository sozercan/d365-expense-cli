package expense

import "testing"

func TestSubmittedReportStatusUsesOnlyExactReportRecord(t *testing.T) {
	body := []byte(`{
		"Messages":[{"Interactions":[
			{"Descriptor":{"Properties":{"ExpNumber_field":"OTHER","expenseReportStatus_dataMethod":"Draft"}}},
			{"Descriptor":{"Properties":{"ExpNumber_field":"ER-42","ApprovalStatus_field":"2"}}}
		]}]
	}`)
	status, found, err := submittedReportStatus(body, "ER-42")
	if err != nil {
		t.Fatal(err)
	}
	if !found || status != "2" {
		t.Fatalf("status=%q found=%t", status, found)
	}
}

func TestSubmittedReportStatusRejectsConflictingTargetEvidence(t *testing.T) {
	body := []byte(`{
		"records":[
			{"ExpNumber_field":"ER-42","ApprovalStatus_field":"2"},
			{"ExpNumber_field":"ER-42","expenseReportStatus_dataMethod":"Draft"}
		]
	}`)
	if _, _, err := submittedReportStatus(body, "ER-42"); err == nil {
		t.Fatal("conflicting status evidence was accepted")
	}
}

func TestIsDraftStatusRecognizesObservedCodeAndLabel(t *testing.T) {
	for _, status := range []string{"Draft", " draft ", "1"} {
		if !isDraftStatus(status) {
			t.Fatalf("isDraftStatus(%q) = false", status)
		}
	}
	for _, status := range []string{"2", "Submitted", "In review", ""} {
		if isDraftStatus(status) {
			t.Fatalf("isDraftStatus(%q) = true", status)
		}
	}
}
