#!/bin/bash

# Test Coverage Report Script
# Generates and displays test coverage percentages

set -e

echo "Running tests with coverage..."
go test -coverprofile=coverage.out ./... 2>&1 | grep -E "^(ok|FAIL|\?)" | grep -v "no test files"

echo ""
echo "=========================================="
echo "       COVERAGE BY PACKAGE"
echo "=========================================="
echo ""

# Show coverage for packages with tests
go tool cover -func=coverage.out | grep -E "^(bloomary-api/.*\.go|total:)" | awk '
/^bloomary-api\/internal\/gates\/repositories\/account/ { repo_account++ }
/^bloomary-api\/internal\/gates\/repositories\/entitlement/ { repo_entitlement++ }
/^bloomary-api\/internal\/gates\/repositories\/trial/ { repo_trial++ }
/^bloomary-api\/internal\/stories/ { stories++ }
/^bloomary-api\/internal\/gates\/linkedin/ { linkedin++ }
/^bloomary-api\// && !/mocks/ && !/tests/ { 
    if ($NF ~ /%$/) {
        pkg = $1
        sub(/:.*/, "", pkg)
        split(pkg, parts, "/")
        if (length(parts) >= 3) {
            category = parts[3]
            if (parts[2] == "gates" && parts[3] == "repositories" && length(parts) >= 4) {
                category = "repositories/" parts[4]
            } else if (parts[2] == "gates") {
                category = "gates/" parts[3]
            } else if (parts[2] == "internal") {
                category = parts[3]
            }
            cov[category] += $NF
            count[category]++
        }
    }
}
END {
    for (cat in cov) {
        if (count[cat] > 0) {
            avg = cov[cat] / count[cat]
            printf "%-40s %.1f%%\n", cat, avg
        }
    }
}' | sort

echo ""
echo "=========================================="
echo "       TOTAL COVERAGE"
echo "=========================================="
go tool cover -func=coverage.out | tail -1 | awk '{printf "Total: %.1f%% of statements\n", $NF}'

echo ""
echo "=========================================="
echo "       TOP UNCOVERED FUNCTIONS"
echo "=========================================="
go tool cover -func=coverage.out | grep "0.0%" | head -10 | awk '{print $1 ":" $2}'

echo ""
echo "HTML report generated: coverage.html"
echo "Open with: open coverage.html"
