package labels_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openshift-hyperfleet/hyperfleet-e2e/pkg/labels"
)

// TestAllE2ETestsHaveRequiredLabels validates that all Ginkgo test specs
// include the three required label dimensions: Priority, Stability, and Scenario
func TestAllE2ETestsHaveRequiredLabels(t *testing.T) {
	// Find the e2e directory relative to this test file
	e2eDir := findE2EDirectory(t)

	// Collect all test specs from e2e directory
	specs, err := collectTestSpecs(e2eDir)
	if err != nil {
		t.Fatalf("Failed to collect test specs: %v", err)
	}

	if len(specs) == 0 {
		t.Fatal("No test specs found in e2e directory - this is unexpected")
	}

	t.Logf("Found %d test specs to validate", len(specs))

	// Validate each spec has required labels
	failures := []string{}
	for _, spec := range specs {
		if err := labels.ValidateLabels(spec.Labels); err != nil {
			failures = append(failures, spec.Name+": "+err.Error())
		}
	}

	// Report all failures
	if len(failures) > 0 {
		t.Errorf("Found %d test specs with missing required labels:\n  - %s",
			len(failures), strings.Join(failures, "\n  - "))
	} else {
		t.Logf("✓ All %d test specs have required labels (Severity)", len(specs))
	}
}

func TestValidateLabels(t *testing.T) {
	tests := []struct {
		name    string
		labels  []string
		wantErr string
	}{
		{name: "severity only", labels: []string{labels.Tier0}},
		{name: "severity with optional labels", labels: []string{labels.Tier2, labels.Negative, labels.Disruptive}},
		{name: "no labels", labels: nil, wantErr: "missing severity label"},
		{name: "optional labels without severity", labels: []string{labels.Negative}, wantErr: "missing severity label"},
		{name: "environment label replaces severity", labels: []string{labels.DesireTransport}},
		{name: "environment label with optional labels", labels: []string{labels.DesireTransport, labels.Slow}},
		{
			name:    "environment label combined with severity",
			labels:  []string{labels.DesireTransport, labels.Tier0},
			wantErr: `environment label "desire-transport" must not be combined with a severity label`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := labels.ValidateLabels(tt.labels)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateLabels(%v) returned error: %v", tt.labels, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateLabels(%v) error = %v, want it to contain %q", tt.labels, err, tt.wantErr)
			}
		})
	}
}

// testSpec represents a Ginkgo test specification with its labels
type testSpec struct {
	Name   string   // Test name from ginkgo.Describe
	Labels []string // Labels from ginkgo.Label()
	File   string   // Source file path
}

// collectTestSpecs walks the e2e directory and extracts all ginkgo.Describe specs with their labels
func collectTestSpecs(e2eDir string) ([]testSpec, error) {
	var specs []testSpec

	err := filepath.Walk(e2eDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories and non-Go files
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}

		// Skip test files (we only want e2e specs which use .go, not _test.go)
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}

		// Parse the Go file
		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}

		// Extract test specs from this file
		fileSpecs := extractSpecsFromFile(node, path)
		specs = append(specs, fileSpecs...)

		return nil
	})

	return specs, err
}

// extractSpecsFromFile extracts all ginkgo.Describe calls with labels from an AST node
func extractSpecsFromFile(node *ast.File, filePath string) []testSpec {
	var specs []testSpec

	// Walk the AST looking for ginkgo.Describe calls
	ast.Inspect(node, func(n ast.Node) bool {
		// Look for call expressions
		callExpr, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		// Check if this is a ginkgo.Describe call
		if !isGinkgoDescribe(callExpr) {
			return true
		}

		// Extract test name and labels
		spec := extractSpecFromDescribe(callExpr, filePath)
		if spec != nil {
			specs = append(specs, *spec)
		}

		return true
	})

	return specs
}

// isGinkgoDescribe checks if a call expression is ginkgo.Describe
func isGinkgoDescribe(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	// Check if the selector is ginkgo.Describe
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}

	return ident.Name == "ginkgo" && sel.Sel.Name == "Describe"
}

// extractSpecFromDescribe extracts test name and labels from a ginkgo.Describe call
// Expected pattern: ginkgo.Describe(name, ginkgo.Label(...), func() { ... })
func extractSpecFromDescribe(call *ast.CallExpr, filePath string) *testSpec {
	if len(call.Args) < 2 {
		return nil
	}

	// First argument should be the test name (string literal or variable)
	testName := extractStringValue(call.Args[0])
	if testName == "" {
		return nil
	}

	// Look for ginkgo.Label calls in the arguments
	var labelValues []string
	for _, arg := range call.Args[1:] {
		if labelCall, ok := arg.(*ast.CallExpr); ok {
			if isGinkgoLabel(labelCall) {
				labels := extractLabelsFromLabelCall(labelCall)
				labelValues = append(labelValues, labels...)
			}
		}
	}

	return &testSpec{
		Name:   testName,
		Labels: labelValues,
		File:   filePath,
	}
}

// isGinkgoLabel checks if a call expression is ginkgo.Label
func isGinkgoLabel(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}

	return ident.Name == "ginkgo" && sel.Sel.Name == "Label"
}

// extractLabelsFromLabelCall extracts label values from a ginkgo.Label() call
func extractLabelsFromLabelCall(call *ast.CallExpr) []string {
	var labelValues []string

	for _, arg := range call.Args {
		// Labels are typically references to constants from labels package
		// Pattern: labels.Tier0, labels.Stable, etc.
		if sel, ok := arg.(*ast.SelectorExpr); ok {
			if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "labels" {
				// Map constant name to actual label value
				labelValue := constantToLabelValue(sel.Sel.Name)
				if labelValue != "" {
					labelValues = append(labelValues, labelValue)
				}
			}
		}
	}

	return labelValues
}

// constantToLabelValue maps label constant names to their actual values
func constantToLabelValue(constName string) string {
	// Map constant names to their string values
	// This must match the constants in pkg/labels/labels.go
	mapping := map[string]string{
		// Severity
		"Tier0": labels.Tier0,
		"Tier1": labels.Tier1,
		"Tier2": labels.Tier2,
		// Scenario
		"Negative":    labels.Negative,
		"Performance": labels.Performance,
		// Functionality
		"Upgrade": labels.Upgrade,
		// Constraint
		"Disruptive": labels.Disruptive,
		"Slow":       labels.Slow,
		// Environment
		"DesireTransport": labels.DesireTransport,
	}

	return mapping[constName]
}

// extractStringValue extracts a string value from an expression
func extractStringValue(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.BasicLit:
		// Direct string literal
		if v.Kind == token.STRING {
			// Remove quotes from string literal
			return strings.Trim(v.Value, `"`)
		}
	case *ast.Ident:
		// Variable reference - we can't determine the value at compile time
		// Return the variable name as a placeholder
		return v.Name
	}
	return ""
}

// findE2EDirectory locates the e2e directory relative to this test file
func findE2EDirectory(t *testing.T) string {
	// This test is in pkg/labels/validate_test.go
	// e2e directory is at ../../e2e relative to this file
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}

	// Navigate up to project root and find e2e directory
	e2eDir := filepath.Join(wd, "..", "..", "e2e")
	if _, err := os.Stat(e2eDir); os.IsNotExist(err) {
		t.Fatalf("e2e directory not found at %s", e2eDir)
	}

	return e2eDir
}
