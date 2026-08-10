package tasks

// Test-only exports for external test package (tasks_test).
// These aliases allow test files that import internal/extractor
// to live in package tasks_test without creating an import cycle.

type (
	BatchAddRPCSnapshots = batchAddRPCSnapshots
	BatchAddRPCRequest   = batchAddRPCRequest
)

var (
	BatchAddSuccessResponse        = batchAddSuccessResponse
	BatchAddTaskListResult         = batchAddTaskListResult
	AssertBatchAddStrings          = assertBatchAddStrings
	AssertBatchAddStringsUnordered = assertBatchAddStringsUnordered
	RedactAssignmentValues         = redactAssignmentValues
)
