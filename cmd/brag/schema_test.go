package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type schemaDocument struct {
	Schema     string                     `json:"$schema"`
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
	Defs       map[string]struct {
		Required          []string                   `json:"required"`
		Properties        map[string]json.RawMessage `json:"properties"`
		DependentRequired map[string][]string        `json:"dependentRequired"`
	} `json:"$defs"`
}

func readSchema(t *testing.T, name string) schemaDocument {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "schema", name))
	if err != nil {
		t.Fatal(err)
	}
	var schema schemaDocument
	if err := json.Unmarshal(b, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Schema == "" {
		t.Fatalf("%s schema dialect missing", name)
	}
	return schema
}

func hasField(fields []string, want string) bool {
	for _, field := range fields {
		if field == want {
			return true
		}
	}
	return false
}

func TestV1SchemaRemainsPhase1Compatible(t *testing.T) {
	schema := readSchema(t, "brag-v1.schema.json")
	if hasField(schema.Required, "engineering_evidence") {
		t.Fatal("v1 schema must not require Phase 2 evidence")
	}
	if _, ok := schema.Properties["engineering_evidence"]; ok {
		t.Fatal("v1 schema must not define the Phase 2 field")
	}
	var version struct {
		Const int `json:"const"`
	}
	if err := json.Unmarshal(schema.Properties["schema_version"], &version); err != nil || version.Const != 1 {
		t.Fatalf("v1 schema_version const=%d err=%v", version.Const, err)
	}
}

func TestV3SchemaDeclaresDynamicRunContract(t *testing.T) {
	schema := readSchema(t, "brag-v3.schema.json")
	if !hasField(schema.Required, "dynamic_tests") {
		t.Fatal("v3 must require dynamic_tests")
	}
	var version struct {
		Const int `json:"const"`
	}
	if json.Unmarshal(schema.Properties["schema_version"], &version) != nil || version.Const != 3 {
		t.Fatalf("v3 schema version=%d", version.Const)
	}
	for _, name := range []string{"dynamic_tests", "dynamic_run", "dynamic_coverage", "dynamic_coverage_comparison", "dynamic_package", "dynamic_package_comparison", "dynamic_run_details", "dynamic_package_comparison_artifact"} {
		if len(schema.Defs[name].Properties) == 0 {
			t.Errorf("v3 dynamic schema definition %s is missing or incomplete", name)
		}
	}
	for _, field := range []string{"snapshot", "run_index", "run_order", "status", "classification", "test_counts", "package_count", "package_counts", "coverage", "artifact_incomplete"} {
		if !hasField(schema.Defs["dynamic_run"].Required, field) {
			t.Errorf("dynamic_run schema must require %s", field)
		}
	}
}

func TestV4SchemaRequiresMutationReportAndArtifacts(t *testing.T) {
	schema := readSchema(t, "brag-v4.schema.json")
	if !hasField(schema.Required, "mutation_tests") {
		t.Fatal("v4 must require mutation_tests")
	}
	for _, name := range []string{"mutation_tests", "mutation_snapshot", "mutation_count_map", "mutation_site", "mutation_manifest_artifact", "mutation_outcome", "mutation_outcome_artifact"} {
		if len(schema.Defs[name].Properties) == 0 {
			t.Errorf("v4 missing mutation definition %s", name)
		}
	}
	for _, field := range []string{"selected", "eligible", "unselected", "unexamined", "population_incomplete", "counts", "score", "status"} {
		if !hasField(schema.Defs["mutation_snapshot"].Required, field) {
			t.Errorf("mutation snapshot missing %s", field)
		}
	}
	for _, field := range []string{"goos", "goarch", "per_run_timeout_millis", "total_timeout_millis", "test_configuration"} {
		if !hasField(schema.Defs["mutation_tests"].Required, field) {
			t.Errorf("v4 mutation_tests must require %s", field)
		}
	}
}

func TestV5SchemaDeclaresContributionSection(t *testing.T) {
	schema := readSchema(t, "brag-v5.schema.json")
	if !hasField(schema.Required, "test_contributions") {
		t.Fatal("v5 must require test_contributions")
	}
	var version struct {
		Const int `json:"const"`
	}
	if json.Unmarshal(schema.Properties["schema_version"], &version) != nil || version.Const != 5 {
		t.Fatalf("v5 schema version=%d", version.Const)
	}
	if len(schema.Defs["test_contributions"].Properties) == 0 {
		t.Fatal("v5 contribution schema missing")
	}
	if len(schema.Defs["contribution_matrix"].Properties) == 0 {
		t.Fatal("v5 contribution matrix artifact schema missing")
	}
	for _, field := range []string{"format_version", "kind", "source_identity", "manifest_sha256", "tests", "baseline_tests", "controls", "pairs", "definition_changes", "history_events", "groups"} {
		if !hasField(schema.Defs["contribution_matrix"].Required, field) {
			t.Errorf("v5 contribution matrix schema must require %s", field)
		}
	}
	for _, field := range []string{"baseline_test_identity", "groups", "baseline_status", "uncommitted_test_versions", "matrix_artifact", "matrix_sha256", "test_detector_counts"} {
		if _, ok := schema.Defs["test_contributions"].Properties[field]; !ok {
			t.Errorf("v5 contribution schema missing %s", field)
		}
	}
	contributions := &testContributionReport{Status: "partial", Tests: []contributionTest{}, BaselineTests: []contributionTest{}, Pairs: []contributionPair{}, Controls: []contributionControl{}, OverlapCandidates: []string{}, SetDerivedMarginalCounts: map[string]int{}, DetectedMutationIDs: []string{}, DetectorCounts: map[string]int{}, ConfirmedUniqueMutationIDs: []string{}, SuiteInteractions: []string{}, DefinitionChanges: []contributionDefinitionChange{}, HistoryEvents: []contributionDefinitionChange{}, Groups: []contributionGroup{}, Limitations: []string{}}
	for _, name := range []string{"contribution_pair", "contribution_control"} {
		for _, field := range []string{"evidence_artifact", "evidence_sha256"} {
			if _, ok := schema.Defs[name].Properties[field]; !ok {
				t.Errorf("v5 %s schema missing %s", name, field)
			}
		}
	}
	for _, ref := range []struct{ definition, artifact, digest string }{
		{"test_contributions", "matrix_artifact", "matrix_sha256"},
		{"contribution_pair", "evidence_artifact", "evidence_sha256"},
		{"contribution_control", "evidence_artifact", "evidence_sha256"},
	} {
		deps := schema.Defs[ref.definition].DependentRequired
		if !hasField(deps[ref.artifact], ref.digest) || !hasField(deps[ref.digest], ref.artifact) {
			t.Errorf("v5 %s must require artifact and digest together", ref.definition)
		}
	}
	for _, ref := range []struct{ definition, field string }{
		{"test_contributions", "manifest_sha256"},
		{"test_contributions", "matrix_sha256"},
		{"contribution_matrix", "manifest_sha256"},
		{"contribution_pair", "evidence_sha256"},
		{"contribution_control", "evidence_sha256"},
	} {
		var fieldSchema struct {
			Pattern string `json:"pattern"`
		}
		if err := json.Unmarshal(schema.Defs[ref.definition].Properties[ref.field], &fieldSchema); err != nil || fieldSchema.Pattern != "^[a-f0-9]{64}$" {
			t.Errorf("v5 %s.%s must constrain lowercase SHA-256", ref.definition, ref.field)
		}
	}
	raw, err := json.Marshal(report{SchemaVersion: 5, TestContributions: contributions})
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	var contributionJSON map[string]json.RawMessage
	if err := json.Unmarshal(result["test_contributions"], &contributionJSON); err != nil {
		t.Fatal(err)
	}
	for _, field := range schema.Defs["test_contributions"].Required {
		if _, ok := contributionJSON[field]; !ok {
			t.Errorf("serialized v5 contribution missing %s", field)
		}
	}
}

func TestV2SchemaDeclaresRequiredReportAndEngineeringFields(t *testing.T) {
	schema := readSchema(t, "brag-v2.schema.json")
	if _, ok := schema.Properties["engineering_evidence"]; !ok || !hasField(schema.Required, "engineering_evidence") {
		t.Fatal("v2 schema must require engineering_evidence")
	}
	var version struct {
		Const int `json:"const"`
	}
	if err := json.Unmarshal(schema.Properties["schema_version"], &version); err != nil || version.Const != reportSchemaVersion {
		t.Fatalf("v2 schema_version const=%d want %d err=%v", version.Const, reportSchemaVersion, err)
	}

	raw, err := json.Marshal(report{SchemaVersion: reportSchemaVersion, ToolVersion: "1.0.0", ConfigVersion: 1, Repository: ".", Scope: "all", History: historyInfo{IncludedCommitIDs: []string{}, Boundary: "test"}, Contributors: []Person{}, RepositoryMeasures: repositoryMeasures{}, WorkingTree: worktree{}, Unavailable: map[string]unavailable{}, Warnings: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	var reportJSON map[string]json.RawMessage
	if err := json.Unmarshal(raw, &reportJSON); err != nil {
		t.Fatal(err)
	}
	for _, key := range schema.Required {
		if _, ok := schema.Properties[key]; !ok {
			t.Errorf("required field %q has no schema property", key)
		}
		if _, ok := reportJSON[key]; !ok {
			t.Errorf("serialized report missing schema-required field %q", key)
		}
	}
	engineering := schema.Defs["engineering_evidence"]
	for _, field := range []string{"algorithm_version", "complexity", "test_inventory", "documentation", "architecture", "security", "duplication"} {
		if _, ok := engineering.Properties[field]; !ok || !hasField(engineering.Required, field) {
			t.Errorf("engineering_evidence schema missing required field %s", field)
		}
	}
	for _, field := range []string{"direct_internal_dependency_distribution", "direct_external_dependency_distribution", "transitive_dependency_distribution", "transitive_only_package_dependency_count", "cyclic_package_components"} {
		if _, ok := schema.Defs["architecture_snapshot"].Properties[field]; !ok {
			t.Errorf("architecture snapshot schema missing %s", field)
		}
	}
	if _, ok := schema.Defs["package_node"].Properties["transitive_only_dependency_count"]; !ok {
		t.Error("package node schema missing transitive_only_dependency_count")
	}
	for _, def := range []string{"architecture_snapshot", "security_snapshot"} {
		if _, ok := schema.Defs[def].Properties["unsupported_language_files"]; !ok {
			t.Errorf("%s schema missing unsupported_language_files", def)
		}
	}
	for _, def := range []string{"complexity_snapshot", "architecture_snapshot", "security_snapshot"} {
		if _, ok := schema.Defs[def].Properties["parse_failures_truncated"]; !ok || !hasField(schema.Defs[def].Required, "parse_failures_truncated") {
			t.Errorf("%s schema must require parse_failures_truncated", def)
		}
	}
	if _, ok := schema.Defs["security_evidence"].Properties["vulnerability_scanner"]; !ok {
		t.Fatal("security scanner status missing from schema")
	}
}
