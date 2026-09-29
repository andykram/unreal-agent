package repl

import (
	"reflect"
	"testing"
)

func TestResumeOptionDoesNotConsumeOtherFlags(t *testing.T) {
	for _, test := range []struct {
		args    []string
		choice  resumeChoice
		cleaned []string
	}{
		{[]string{"--resume", "--workspace", "/tmp/project"}, resumeChoice{Picker: true}, []string{"--workspace", "/tmp/project"}},
		{[]string{"--workspace", "/tmp/project", "--resume", "Named session"}, resumeChoice{Name: "Named session"}, []string{"--workspace", "/tmp/project"}},
		{[]string{"--resume=Name with spaces"}, resumeChoice{Name: "Name with spaces"}, nil},
	} {
		choice, cleaned, err := parseResumeOption(test.args)
		if err != nil || choice != test.choice || !reflect.DeepEqual(cleaned, test.cleaned) && !(len(cleaned) == 0 && len(test.cleaned) == 0) {
			t.Fatalf("parse %q = %#v, %q, %v", test.args, choice, cleaned, err)
		}
	}
	if _, _, err := parseResumeOption([]string{"--resume", "--resume=name"}); err == nil {
		t.Fatal("duplicate resume option was accepted")
	}
}
