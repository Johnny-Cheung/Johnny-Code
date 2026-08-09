package config

import "testing"




func TestGetContextWindow_ConfigWins(t *testing.T) {
	p := &ProviderConfig{Model: "claude-sonnet-4-6", ContextWindow: 12345}
	if got := p.GetContextWindow(); got != 12345 {
		t.Fatalf("config value should win: got %d, want 12345", got)
	}

	
	p.SetFetchedContextWindow(999999)
	if got := p.GetContextWindow(); got != 12345 {
		t.Fatalf("config value should beat fetched value: got %d, want 12345", got)
	}
}



func TestGetContextWindow_FetchedBeatsMapping(t *testing.T) {
	p := &ProviderConfig{Model: "claude-sonnet-4-6"} 
	p.SetFetchedContextWindow(321000)
	if got := p.GetContextWindow(); got != 321000 {
		t.Fatalf("fetched value should beat mapping table: got %d, want 321000", got)
	}

	
	p2 := &ProviderConfig{Model: "gpt-4o"}
	p2.SetFetchedContextWindow(0)
	if got := p2.GetContextWindow(); got != 128000 {
		t.Fatalf("zero fetched value should be ignored: got %d, want 128000", got)
	}
}




func TestGetContextWindow_MappingTable(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		
		{"claude-sonnet-4-6-1m", 1000000},
		{"claude-opus-4-6-1m", 1000000},
		{"some-model-1m", 1000000},
		
		{"gpt-4.1", 1000000},
		{"gpt-4.1-mini", 1000000},
		{"gpt-4o", 128000},
		{"gpt-4o-mini", 128000},
		{"gpt-4-turbo", 128000},
		{"o1", 200000},
		{"o1-preview", 200000},
		{"o3-mini", 200000},
		{"o4-mini", 200000},
		{"gpt-3.5-turbo", 16385},
		
		{"claude-sonnet-4-6", 200000},
		{"claude-3-5-haiku-20241022", 200000},
		
		{"glm-4.7", 128000},
		{"some-unknown-model", 128000},
		{"", 128000},
	}
	for _, tt := range tests {
		p := &ProviderConfig{Model: tt.model}
		if got := p.GetContextWindow(); got != tt.want {
			t.Errorf("GetContextWindow(model=%q) = %d, want %d", tt.model, got, tt.want)
		}
	}
}




func TestLookupModelContextWindow_NoMatch(t *testing.T) {
	if got := lookupModelContextWindow("totally-unknown"); got != 0 {
		t.Fatalf("expected 0 for unknown model, got %d", got)
	}
}
