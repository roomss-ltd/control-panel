package main

import "testing"

func TestFeatureMilestonesAndTests(t *testing.T) {
	ms := parseMilestones("- [x] loose step\n## M1 Stage rollout\n- [x] Terraform root @Infra Changes\n- [~] Release/CI v1 @Release CI/CD\n- [ ] acceptance\n\n## M2 Production\n1. [ ] prod rollout\n")
	if len(ms) != 3 || ms[0].Title != "Plan" || ms[1].Title != "M1 Stage rollout" || ms[1].Done != 1 || len(ms[1].Steps) != 3 || len(ms[2].Steps) != 1 {
		t.Fatalf("milestones: %+v", ms)
	}
	if s := ms[1].Steps[1]; s.State != "doing" || s.Owner != "Release CI/CD" || s.Text != "Release/CI v1" {
		t.Fatalf("step: %+v", s)
	}
	n := parseTests("- [x] a\n- [!] b\n- [-] c\n- [ ] d\n- [ ] e\n")
	if n["pass"] != 1 || n["fail"] != 1 || n["skip"] != 1 || n["todo"] != 2 {
		t.Fatalf("tests: %v", n)
	}
}
