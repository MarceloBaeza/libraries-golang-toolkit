package audit

import (
	"strings"
	"testing"
)

func TestArgsForRequiresModuleActionAndEntityType(t *testing.T) {
	tests := []struct {
		name  string
		entry Entry
	}{
		{"missing module", Entry{Action: "create", EntityType: "movement"}},
		{"missing action", Entry{Module: "finance", EntityType: "movement"}},
		{"missing entity type", Entry{Module: "finance", Action: "create"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := argsFor(tt.entry); err == nil {
				t.Fatal("expected an error for an incomplete entry")
			}
		})
	}
}

func TestArgsForDefaultsResultToSuccess(t *testing.T) {
	args, err := argsFor(Entry{Module: "finance", Action: "create", EntityType: "movement"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	const resultPosition = 5 // user_id, module, action, entity_type, entity_id, result
	if got := args[resultPosition]; got != string(ResultSuccess) {
		t.Errorf("result = %v, want %q", got, ResultSuccess)
	}
}

func TestArgsForTurnsEmptyStringsIntoNull(t *testing.T) {
	args, err := argsFor(Entry{Module: "finance", Action: "create", EntityType: "movement"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// user_id, entity_id, ip_address and user_agent were left empty.
	for _, pos := range []int{0, 4, 8, 9} {
		if args[pos] != nil {
			t.Errorf("args[%d] = %v, want nil (SQL NULL)", pos, args[pos])
		}
	}
}

func TestArgsForOrdersParametersToMatchTheInsert(t *testing.T) {
	entry := Entry{
		UserID:     "user-1",
		Module:     "finance",
		Action:     "update",
		EntityType: "movement",
		EntityID:   "movement-9",
		Result:     ResultDenied,
		OldValue:   map[string]any{"amount": 100},
		NewValue:   map[string]any{"amount": 200},
		IPAddress:  "203.0.113.4",
		UserAgent:  "curl/8.0",
	}

	args, err := argsFor(entry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []any{
		"user-1", "finance", "update", "movement", "movement-9",
		string(ResultDenied), `{"amount":100}`, `{"amount":200}`, "203.0.113.4", "curl/8.0",
	}
	if len(args) != len(want) {
		t.Fatalf("got %d args, want %d", len(args), len(want))
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("args[%d] = %v, want %v", i, args[i], want[i])
		}
	}
}

// A value json.Marshal can't encode must surface as an error, not silently
// become NULL — this is the exact bug the original "_, _ = json.Marshal"
// pattern had.
func TestArgsForReturnsErrorWhenValueCannotBeMarshaled(t *testing.T) {
	entry := Entry{
		Module: "finance", Action: "create", EntityType: "movement",
		OldValue: map[string]any{"bad": make(chan int)},
	}

	_, err := argsFor(entry)
	if err == nil {
		t.Fatal("expected a marshal error")
	}
	if !strings.Contains(err.Error(), "old value") {
		t.Errorf("error = %q, want it to name which value failed to marshal", err)
	}
}
