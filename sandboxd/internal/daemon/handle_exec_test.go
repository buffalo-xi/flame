package daemon

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The two start paths of the simplex endpoint: direct (argv passed through) and the language path (the interpreter
// resolved by sandboxd per mode), plus their mutual exclusion — that boundary is exactly "who owns the knowledge of
// the interpreter entry".
func TestBuildExecArgv(t *testing.T) {
	resolve := func(lang string) (string, error) {
		if lang == "lua" {
			return "/usr/local/bin/lua", nil
		}
		return "", fmt.Errorf("%w: %q", ErrUnsupportedLang, lang)
	}

	t.Run("direct: argv is returned unchanged", func(t *testing.T) {
		got, err := buildExecArgv("", []string{"sh", "-c", "echo hi"}, "", nil, resolve)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Join(got, " ") != "sh -c echo hi" {
			t.Errorf("argv = %v", got)
		}
	})

	t.Run("direct: an empty argv is an error", func(t *testing.T) {
		if _, err := buildExecArgv("", nil, "", nil, resolve); err == nil {
			t.Error("want error for empty argv, got nil")
		}
	})

	t.Run("language path: assembles interpreter + script + args", func(t *testing.T) {
		got, err := buildExecArgv("lua", nil, "/skill/scripts/main.lua", []string{"a", "b"}, resolve)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "/usr/local/bin/lua /skill/scripts/main.lua a b"
		if strings.Join(got, " ") != want {
			t.Errorf("argv = %q, want %q", strings.Join(got, " "), want)
		}
	})

	t.Run("language path: lang and argv are mutually exclusive", func(t *testing.T) {
		if _, err := buildExecArgv("lua", []string{"lua", "/x.lua"}, "/x.lua", nil, resolve); err == nil {
			t.Error("want error when both lang and argv are set, got nil (it would assemble lua lua /x.lua)")
		}
	})

	t.Run("language path: a missing script path is an error", func(t *testing.T) {
		if _, err := buildExecArgv("lua", nil, "", nil, resolve); err == nil {
			t.Error("want error when script is empty, got nil")
		}
	})

	t.Run("language path: an undefined language keeps the sentinel error", func(t *testing.T) {
		_, err := buildExecArgv("ruby", nil, "/x.rb", nil, resolve)
		if !errors.Is(err, ErrUnsupportedLang) {
			t.Errorf("err = %v, want ErrUnsupportedLang (it drives the unsupported_lang protocol error code)", err)
		}
	})
}

// An undefined language, or one whose command is not configured for the current mode, must both fail (startup and
// request behaviour alike).
func TestResolveLangCommand(t *testing.T) {
	cfg := &Config{
		Mode: "docker",
		Langs: map[string]LangDef{
			"lua":  {Command: "lua"},
			"ruby": {CommandByMode: map[string]string{"none": "ruby.exe"}},
		},
	}
	if got, err := cfg.ResolveLangCommand("lua"); err != nil || got != "lua" {
		t.Errorf("lua = %q, %v; want lua, nil", got, err)
	}
	if _, err := cfg.ResolveLangCommand("go"); !errors.Is(err, ErrUnsupportedLang) {
		t.Errorf("an undefined language should return ErrUnsupportedLang, got %v", err)
	}
	// Defined but unresolvable in the current mode → not unsupported_lang, but a missing config entry
	if _, err := cfg.ResolveLangCommand("ruby"); err == nil {
		t.Error("want error for a mode without a matching command, got nil")
	}
}
