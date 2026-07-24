package concurrent

import (
	"testing"

	"goaria-v3/internal/surge/types"
)

// TestMergeOverlappingTasks verifies the union-merge dedup used by handlePause.
func TestMergeOverlappingTasks(t *testing.T) {
	cases := []struct {
		name  string
		input []types.Task
		want  []types.Task
	}{
		{
			name:  "empty",
			input: []types.Task{},
			want:  []types.Task{},
		},
		{
			name:  "single",
			input: []types.Task{{Offset: 0, Length: 100}},
			want:  []types.Task{{Offset: 0, Length: 100}},
		},
		{
			name:  "disjoint",
			input: []types.Task{{Offset: 0, Length: 100}, {Offset: 200, Length: 50}},
			want:  []types.Task{{Offset: 0, Length: 100}, {Offset: 200, Length: 50}},
		},
		{
			name:  "overlapping",
			input: []types.Task{{Offset: 0, Length: 100}, {Offset: 50, Length: 100}},
			want:  []types.Task{{Offset: 0, Length: 150}},
		},
		{
			name:  "adjacent",
			input: []types.Task{{Offset: 0, Length: 100}, {Offset: 100, Length: 50}},
			want:  []types.Task{{Offset: 0, Length: 150}},
		},
		{
			name:  "fully_contained",
			input: []types.Task{{Offset: 0, Length: 100}, {Offset: 20, Length: 10}},
			want:  []types.Task{{Offset: 0, Length: 100}},
		},
		{
			name:  "same_offset_diff_length",
			input: []types.Task{{Offset: 0, Length: 100}, {Offset: 0, Length: 50}},
			want:  []types.Task{{Offset: 0, Length: 100}},
		},
		{
			name:  "chain_overlap",
			input: []types.Task{{Offset: 0, Length: 100}, {Offset: 50, Length: 100}, {Offset: 120, Length: 50}},
			want:  []types.Task{{Offset: 0, Length: 170}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeOverlappingTasks(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i].Offset != tc.want[i].Offset || got[i].Length != tc.want[i].Length {
					t.Errorf("task %d: offset=%d length=%d, want offset=%d length=%d",
						i, got[i].Offset, got[i].Length, tc.want[i].Offset, tc.want[i].Length)
				}
			}
		})
	}
}
