package schedules

import "testing"

func TestNormalizeTrigger(t *testing.T) {
	cases := []struct {
		in   string
		want Trigger
	}{
		{"manual", TriggerManual},
		{"cron", TriggerCron},
		{"event", TriggerEvent},
		{"Cron", ""}, // case-sensitive, anything unknown falls back to empty
		{"", ""},
		{"whenever", ""},
	}
	for _, c := range cases {
		if got := NormalizeTrigger(c.in); got != c.want {
			t.Errorf("NormalizeTrigger(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if len(Triggers) != 3 {
		t.Errorf("Triggers = %d, want 3", len(Triggers))
	}
}
