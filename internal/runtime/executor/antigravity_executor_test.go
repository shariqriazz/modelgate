package executor

import "testing"

func TestAntigravityModelName2Alias(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "maps high preview alias", input: "gemini-3-pro-high", want: "gemini-3-pro-preview"},
		{name: "maps 3.1 high preview alias", input: "gemini-3.1-pro-high", want: "gemini-3.1-pro-preview"},
		{name: "maps flash preview alias", input: "gemini-3-flash", want: "gemini-3-flash-preview"},
		{name: "keeps 2.5 pro exposed", input: "gemini-2.5-pro", want: "gemini-2.5-pro"},
		{name: "keeps 2.5 flash thinking exposed", input: "gemini-2.5-flash-thinking", want: "gemini-2.5-flash-thinking"},
		{name: "keeps 3 pro low exposed", input: "gemini-3-pro-low", want: "gemini-3-pro-low"},
		{name: "keeps 3.1 pro low exposed", input: "gemini-3.1-pro-low", want: "gemini-3.1-pro-low"},
		{name: "ignores stale computer use model", input: "rev19-uic3-1p", want: ""},
		{name: "ignores tab flash lite preview", input: "tab_flash_lite_preview", want: ""},
		{name: "ignores tab jump flash lite preview", input: "tab_jump_flash_lite_preview", want: ""},
		{name: "ignores chat 20706", input: "chat_20706", want: ""},
		{name: "ignores chat 23310", input: "chat_23310", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := modelName2Alias(tt.input); got != tt.want {
				t.Fatalf("modelName2Alias(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestAntigravityAlias2ModelName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "maps 3 pro preview back to high", input: "gemini-3-pro-preview", want: "gemini-3-pro-high"},
		{name: "maps 3.1 pro preview back to high", input: "gemini-3.1-pro-preview", want: "gemini-3.1-pro-high"},
		{name: "maps flash preview back", input: "gemini-3-flash-preview", want: "gemini-3-flash"},
		{name: "maps opus alias back", input: "gemini-claude-opus-4-6-thinking", want: "claude-opus-4-6-thinking"},
		{name: "removed computer use alias no longer remaps", input: "gemini-2.5-computer-use-preview-10-2025", want: "gemini-2.5-computer-use-preview-10-2025"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := alias2ModelName(tt.input); got != tt.want {
				t.Fatalf("alias2ModelName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
