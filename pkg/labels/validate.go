package labels

import "fmt"

// ValidateLabels verifies that tests contain all required label dimensions.
//
// A spec needs a severity label unless it carries an environment label instead.
// An environment label marks a spec whose stack the tier jobs do not deploy, so it
// must not also carry a severity label: a tier label filter would select it there.
func ValidateLabels(testLabels []string) error {
	hasSeverity := false
	environment := ""

	for _, label := range testLabels {
		switch label {
		// Severity dimension (required unless an environment label is present)
		case Tier0, Tier1, Tier2:
			hasSeverity = true
		// Scenario dimension (optional)
		case Negative, Performance:
			// Optional, no validation needed
		// Functionality dimension (optional)
		case Upgrade:
			// Optional, no validation needed
		// Constraint dimension (optional)
		case Disruptive, Slow:
			// Optional, no validation needed
		// Environment dimension (replaces severity)
		case DesireTransport:
			environment = label
		}
	}

	if environment != "" {
		if hasSeverity {
			return fmt.Errorf("environment label %q must not be combined with a severity label (%s/%s/%s): tier jobs do not deploy its stack",
				environment, Tier0, Tier1, Tier2)
		}
		return nil
	}

	if !hasSeverity {
		return fmt.Errorf("missing severity label (%s/%s/%s)", Tier0, Tier1, Tier2)
	}

	return nil
}
