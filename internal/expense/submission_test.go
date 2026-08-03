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

func TestSubmittedReportStatusRejectsConflictingPropertiesInTargetRecord(t *testing.T) {
	body := []byte(`{
		"records":[
			{"ExpNumber_field":"ER-42","ApprovalStatus_field":"1","expenseReportStatus_dataMethod":"Submitted"}
		]
	}`)
	if _, _, err := submittedReportStatus(body, "ER-42"); err == nil {
		t.Fatal("conflicting status properties were accepted")
	}
}

func TestSubmittedReportStatusNormalizesEquivalentSubmittedEvidence(t *testing.T) {
	body := []byte(`{
		"records":[
			{"ExpNumber_field":"ER-42","ApprovalStatus_field":"2"},
			{"ExpNumber_field":"ER-42","expenseReportStatus_dataMethod":"Submitted"}
		]
	}`)
	status, found, err := submittedReportStatus(body, "ER-42")
	if err != nil {
		t.Fatal(err)
	}
	if !found || status != "2" {
		t.Fatalf("status=%q found=%t", status, found)
	}
}

func TestSubmittedReportStatusCanonicalizesEquivalentDisplayEvidence(t *testing.T) {
	for _, statuses := range [][2]string{{"2", "Submitted"}, {"Submitted", "2"}} {
		body := []byte(`{"records":[` +
			`{"ExpNumber_field":"ER-42","expenseReportStatus_dataMethod":"` + statuses[0] + `"},` +
			`{"ExpNumber_field":"ER-42","expenseReportStatus_dataMethod":"` + statuses[1] + `"}` +
			`]}`)
		status, found, err := submittedReportStatus(body, "ER-42")
		if err != nil {
			t.Fatal(err)
		}
		if !found || status != "Submitted" {
			t.Fatalf("statuses=%q status=%q found=%t", statuses, status, found)
		}
	}
}

func TestSubmittedReportStatusPrefersStableCodeOverLocalizedDisplay(t *testing.T) {
	body := []byte(`{
		"records":[
			{"ExpNumber_field":"ER-42","ApprovalStatus_field":"2","expenseReportStatus_dataMethod":"Eingereicht"}
		]
	}`)
	status, found, err := submittedReportStatus(body, "ER-42")
	if err != nil {
		t.Fatal(err)
	}
	if !found || status != "2" {
		t.Fatalf("status=%q found=%t", status, found)
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

func TestIsSubmittedStatusRequiresModeledCodeOrLabel(t *testing.T) {
	for _, status := range []string{"Submitted", " submitted ", "2"} {
		if !isSubmittedStatus(status) {
			t.Fatalf("isSubmittedStatus(%q) = false", status)
		}
	}
	for _, status := range []string{"Draft", "1", "Approved", "In review", "3", ""} {
		if isSubmittedStatus(status) {
			t.Fatalf("isSubmittedStatus(%q) = true", status)
		}
	}
}
