package hosts

import "testing"

func TestUpsertEntry(t *testing.T) {
	const marker = "# nanokube-managed"
	in := "127.0.0.1 localhost\n10.0.0.9 cp.example.test " + marker + "\n"
	got := upsertEntry(in, "cp.example.test", "10.0.2.10")
	want := "127.0.0.1 localhost\n10.0.2.10 cp.example.test " + marker + "\n"
	if got != want {
		t.Errorf("rewrite: got %q, want %q", got, want)
	}

	got = upsertEntry("127.0.0.1 localhost\n", "cp.example.test", "10.0.2.10")
	want = "127.0.0.1 localhost\n10.0.2.10 cp.example.test " + marker + "\n"
	if got != want {
		t.Errorf("append: got %q, want %q", got, want)
	}
}

func TestHasManagedEntry(t *testing.T) {
	const marker = "# nanokube-managed"
	in := "127.0.0.1 localhost\n10.0.0.9 cp.example.test " + marker + "\n"
	if !hasManagedEntry(in, "cp.example.test") {
		t.Error("managed entry not detected")
	}
	if hasManagedEntry(in, "other.example.test") {
		t.Error("false positive for unmanaged name")
	}
	if hasManagedEntry("10.0.0.9 cp.example.test\n", "cp.example.test") {
		t.Error("unmarked line must not count as managed")
	}
}
