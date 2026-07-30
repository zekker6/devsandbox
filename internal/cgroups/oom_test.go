package cgroups

import "testing"

func TestParseMemoryEvents(t *testing.T) {
	tests := []struct {
		name string
		data string
		want OOMStats
	}{
		{
			name: "a quiet cgroup reports no kills",
			data: "low 0\nhigh 0\nmax 0\noom 0\noom_kill 0\noom_group_kill 0\n",
		},
		{
			name: "oom_kill counts processes killed",
			data: "low 0\nhigh 12\nmax 3\noom 2\noom_kill 2\noom_group_kill 0\n",
			want: OOMStats{Kills: 2},
		},
		{
			name: "oom_group_kill is tracked separately",
			data: "oom_kill 4\noom_group_kill 1\n",
			want: OOMStats{Kills: 4, GroupKills: 1},
		},
		{
			// The kernel gains memory.events keys across versions, and a
			// truncated read must not discard the counters that did parse.
			name: "unknown keys and unparseable lines are skipped",
			data: "future_key 9\noom_kill 3\nragged\noom_group_kill notanumber\n",
			want: OOMStats{Kills: 3},
		},
		{
			name: "an empty file reports no kills",
			data: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseMemoryEvents(tt.data); got != tt.want {
				t.Errorf("parseMemoryEvents() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestOOMStatsAny(t *testing.T) {
	tests := []struct {
		name  string
		stats OOMStats
		want  bool
	}{
		{name: "zero", stats: OOMStats{}, want: false},
		{name: "process kill", stats: OOMStats{Kills: 1}, want: true},
		{name: "group kill only", stats: OOMStats{GroupKills: 1}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.stats.Any(); got != tt.want {
				t.Errorf("Any() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Callers that could not start a watcher - an unsupported host, a sandbox with no
// cgroup of its own - hold a nil one and must not need a nil check at each use.
func TestNilOOMWatcherIsUsable(t *testing.T) {
	var w *OOMWatcher

	if got := w.Stats(); got != (OOMStats{}) {
		t.Errorf("Stats() on a nil watcher = %+v, want the zero value", got)
	}
	w.Stop()
}

// Ownership decides what a watch does with the counters it finds at attach time,
// and it exists because the two answers are both right for different cgroups.
func TestOwnershipBaseline(t *testing.T) {
	initial := OOMStats{Kills: 4, GroupKills: 1}

	// A cgroup created for this launch: a kill already counted there happened to
	// this sandbox, during the window before the watch attached.
	if got := CgroupFresh.baseline(initial); got != (OOMStats{}) {
		t.Errorf("CgroupFresh.baseline() = %+v, want zero so existing kills are reported", got)
	}

	// A cgroup that outlives the launch: those kills belong to an earlier session.
	if got := CgroupReused.baseline(initial); got != initial {
		t.Errorf("CgroupReused.baseline() = %+v, want the counters found at attach time", got)
	}
}

func TestSince(t *testing.T) {
	tests := []struct {
		name      string
		base, cur OOMStats
		want      OOMStats
	}{
		{
			name: "no baseline counts absolutely",
			cur:  OOMStats{Kills: 2},
			want: OOMStats{Kills: 2},
		},
		{
			name: "an inherited tally is not attributed to this launch",
			base: OOMStats{Kills: 5, GroupKills: 1},
			cur:  OOMStats{Kills: 7, GroupKills: 1},
			want: OOMStats{Kills: 2},
		},
		{
			// A cgroup removed and recreated under the same path would otherwise
			// report fewer than no kills.
			name: "a counter that went backwards clamps to zero",
			base: OOMStats{Kills: 5},
			cur:  OOMStats{Kills: 1},
			want: OOMStats{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := since(tt.base, tt.cur); got != tt.want {
				t.Errorf("since() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
